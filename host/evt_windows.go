package host

import (
	"context"
	"encoding/xml"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// The Event Log API, wevtapi.dll. Its handles are used from the one OS thread
// that opened them, as the API expects: every call locks the goroutine to its
// thread for its duration.
var (
	wevtapi                         = systemDLL("wevtapi.dll")
	procEvtQuery                    = wevtapi.NewProc("EvtQuery")
	procEvtNext                     = wevtapi.NewProc("EvtNext")
	procEvtRender                   = wevtapi.NewProc("EvtRender")
	procEvtClose                    = wevtapi.NewProc("EvtClose")
	procEvtOpenPublisherMetadata    = wevtapi.NewProc("EvtOpenPublisherMetadata")
	procEvtFormatMessage            = wevtapi.NewProc("EvtFormatMessage")
	procEvtOpenLog                  = wevtapi.NewProc("EvtOpenLog")
	procEvtGetLogInfo               = wevtapi.NewProc("EvtGetLogInfo")
	procEvtOpenChannelConfig        = wevtapi.NewProc("EvtOpenChannelConfig")
	procEvtGetChannelConfigProperty = wevtapi.NewProc("EvtGetChannelConfigProperty")
)

const (
	evtQueryChannelPath      = 0x1
	evtQueryForwardDirection = 0x100
	evtQueryReverseDirection = 0x200
	evtRenderEventXml        = 1
	evtFormatMessageEvent    = 1
	evtOpenChannelPath       = 1
	evtLogOldestRecordNumber = 6 // EVT_LOG_PROPERTY_ID
	evtRetention             = 6 // EVT_CHANNEL_CONFIG_PROPERTY_ID: EvtChannelLoggingConfigRetention
	evtAutoBackup            = 7 // EvtChannelLoggingConfigAutoBackup
	evtMaxSize               = 8 // EvtChannelLoggingConfigMaxSize

	errorNoMoreItems         = syscall.Errno(259)
	errorTimeout             = syscall.Errno(1460)
	errorInsufficientBuffer  = syscall.Errno(122)
	errorEvtChannelNotFound  = syscall.Errno(15007)
	errorEvtUnresolvedInsert = syscall.Errno(15029) // the message is formatted, an insert left as is
	errorEvtUnresolvedParam  = syscall.Errno(15030) // ERROR_EVT_UNRESOLVED_PARAMETER_INSERT, same
	errorEvtMaxInserts       = syscall.Errno(15031) // ERROR_EVT_MAX_INSERTS_REACHED, same
	nextTimeout              = 1000                 // ms EvtNext waits, so that ctx is seen within a second
)

// evtVariant is EVT_VARIANT: an 8-byte union, a count and a type.
type evtVariant struct {
	Value       uint64
	Count, Type uint32
}

func evtClose(h uintptr) { procEvtClose.Call(h) }

func evtErr(fn string, err error) error {
	if err == errorEvtChannelNotFound {
		return ErrChannelNotFound
	}
	return fmt.Errorf("%s: %w", fn, err)
}

// evtQuery runs xpath on channel newest first, in batches of 100. Without
// format, every message is "" and EvtFormatMessage is not called.
func evtQuery(ctx context.Context, channel, xpath string, format bool, each func(eventXML []byte, message string) bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pub := map[string]uintptr{} // publisher metadata by provider, 0 when it would not open
	defer func() {
		for _, h := range pub {
			if h != 0 {
				evtClose(h)
			}
		}
	}()
	return evtEach(ctx, channel, xpath, evtQueryReverseDirection, func(ev uintptr, x []byte) bool {
		if !format {
			return each(x, "")
		}
		return each(x, formatMessage(pub, ev, x))
	})
}

// evtEach runs one query and hands each event's handle and XML to f until it
// returns false, the events run out or ctx is done, which returns ctx.Err().
func evtEach(ctx context.Context, channel, xpath string, direction uintptr, f func(ev uintptr, x []byte) bool) error {
	c, err := syscall.UTF16PtrFromString(channel)
	if err != nil {
		return err
	}
	q, err := syscall.UTF16PtrFromString(xpath)
	if err != nil {
		return err
	}
	rs, _, e := procEvtQuery.Call(0, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(q)), evtQueryChannelPath|direction)
	if rs == 0 {
		return evtErr("EvtQuery", e)
	}
	defer evtClose(rs)
	var events [100]uintptr
	buf := make([]uint16, 4096)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var n uint32
		r, _, e := procEvtNext.Call(rs, uintptr(len(events)), uintptr(unsafe.Pointer(&events[0])), nextTimeout, 0, uintptr(unsafe.Pointer(&n)))
		if r == 0 {
			switch e {
			case errorNoMoreItems:
				return nil
			case errorTimeout:
				continue
			}
			return evtErr("EvtNext", e)
		}
		more := true
		var failure error
		for _, ev := range events[:n] {
			if more && failure == nil {
				var x []byte
				if x, buf, failure = renderXML(ev, buf); failure == nil {
					more = f(ev, x)
				}
			}
			evtClose(ev)
		}
		if failure != nil {
			return failure
		}
		if !more {
			return nil
		}
	}
}

// renderXML renders an event as XML, in UTF-8, growing buf when needed.
func renderXML(ev uintptr, buf []uint16) ([]byte, []uint16, error) {
	for {
		var used, props uint32
		r, _, e := procEvtRender.Call(0, ev, evtRenderEventXml, uintptr(len(buf)*2), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&props)))
		if r != 0 {
			return []byte(syscall.UTF16ToString(buf[:used/2])), buf, nil
		}
		if e != errorInsufficientBuffer {
			return nil, buf, evtErr("EvtRender", e)
		}
		buf = make([]uint16, used/2+1)
	}
}

// formatMessage is the event's rendered text, "" when its publisher or its
// message cannot be found, as when the provider's message file is gone.
func formatMessage(pub map[string]uintptr, ev uintptr, x []byte) string {
	var p struct {
		System struct {
			Provider struct {
				Name string `xml:"Name,attr"`
			} `xml:"Provider"`
		} `xml:"System"`
	}
	if xml.Unmarshal(escapeControl(x), &p) != nil || p.System.Provider.Name == "" {
		return ""
	}
	name := p.System.Provider.Name
	h, ok := pub[name]
	if !ok {
		if n, err := syscall.UTF16PtrFromString(name); err == nil {
			h, _, _ = procEvtOpenPublisherMetadata.Call(0, uintptr(unsafe.Pointer(n)), 0, 0, 0)
		}
		pub[name] = h
	}
	if h == 0 {
		return ""
	}
	buf := make([]uint16, 1024)
	for {
		var used uint32
		r, _, e := procEvtFormatMessage.Call(h, ev, 0, 0, 0, evtFormatMessageEvent, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&used)))
		switch {
		case r != 0, partlyFormatted(e) && used > 0:
			return syscall.UTF16ToString(buf[:min(int(used), len(buf))])
		case e == errorInsufficientBuffer && int(used) > len(buf):
			buf = make([]uint16, used)
		default:
			return ""
		}
	}
}

// partlyFormatted is true for the errors that come with a usable message text.
func partlyFormatted(e error) bool {
	return e == errorEvtUnresolvedInsert || e == errorEvtUnresolvedParam || e == errorEvtMaxInserts
}

// evtCoverage reads the time of the channel's oldest record and its logging
// configuration.
func evtCoverage(channel string) (Coverage, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c, err := syscall.UTF16PtrFromString(channel)
	if err != nil {
		return Coverage{}, err
	}
	var cov Coverage

	cfg, _, e := procEvtOpenChannelConfig.Call(0, uintptr(unsafe.Pointer(c)), 0)
	if cfg == 0 {
		return cov, evtErr("EvtOpenChannelConfig", e)
	}
	defer evtClose(cfg)
	prop := func(id uintptr) (evtVariant, error) {
		var v evtVariant
		var used uint32
		if r, _, e := procEvtGetChannelConfigProperty.Call(cfg, id, 0, unsafe.Sizeof(v), uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Pointer(&used))); r == 0 {
			return v, evtErr("EvtGetChannelConfigProperty", e)
		}
		return v, nil
	}
	for _, p := range []struct {
		id  uintptr
		set func(evtVariant)
	}{
		{evtRetention, func(v evtVariant) { cov.Retention = uint32(v.Value) != 0 }},
		{evtAutoBackup, func(v evtVariant) { cov.AutoBackup = uint32(v.Value) != 0 }},
		{evtMaxSize, func(v evtVariant) { cov.MaxSizeBytes = v.Value }},
	} {
		v, err := prop(p.id)
		if err != nil {
			return cov, err
		}
		p.set(v)
	}

	log, _, e := procEvtOpenLog.Call(0, uintptr(unsafe.Pointer(c)), evtOpenChannelPath)
	if log == 0 {
		return cov, evtErr("EvtOpenLog", e)
	}
	defer evtClose(log)
	var v evtVariant
	var used uint32
	if r, _, e := procEvtGetLogInfo.Call(log, evtLogOldestRecordNumber, unsafe.Sizeof(v), uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Pointer(&used))); r == 0 {
		return cov, evtErr("EvtGetLogInfo", e)
	}
	if v.Value == 0 { // an empty log
		return cov, nil
	}
	err = evtEach(context.Background(), channel, fmt.Sprintf("*[System[EventRecordID=%d]]", v.Value), evtQueryForwardDirection, func(_ uintptr, x []byte) bool {
		if ev, err := ParseEventXML(x); err == nil {
			cov.OldestUTC = ev.TimeUTC
		}
		return false
	})
	return cov, err
}
