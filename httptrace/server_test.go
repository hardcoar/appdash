package httptrace

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"sourcegraph.com/sourcegraph/apptrace"
)

var _ apptrace.Event = ServerEvent{}

func TestNewServerEvent(t *testing.T) {
	r := &http.Request{
		Host:          "example.com",
		Method:        "GET",
		URL:           &url.URL{Path: "/foo"},
		Proto:         "HTTP/1.1",
		RemoteAddr:    "127.0.0.1",
		ContentLength: 0,
		Header: http.Header{
			"Authorization": []string{"Basic seeecret"},
			"Accept":        []string{"application/json"},
		},
		Trailer: http.Header{
			"Authorization": []string{"Basic seeecret"},
			"Connection":    []string{"close"},
		},
	}
	e := NewServerEvent(r)
	e.Response.StatusCode = 200
	if e.Schema() != "HTTPServer" {
		t.Errorf("unexpected schema: %v", e.Schema())
	}
	anns, err := apptrace.MarshalEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{
		"_schema:HTTPServer":            "",
		"Request.Headers.Connection":    "close",
		"Request.Headers.Accept":        "application/json",
		"Request.Headers.Authorization": "REDACTED",
		"Request.Proto":                 "HTTP/1.1",
		"Request.RemoteAddr":            "127.0.0.1",
		"Request.Host":                  "example.com",
		"Request.ContentLength":         "0",
		"Request.Method":                "GET",
		"Request.URI":                   "/foo",
		"Response.StatusCode":           "200",
		"Response.ContentLength":        "0",
		"User":       "",
		"Route":      "",
		"ServerSend": "0001-01-01T00:00:00Z",
		"ServerRecv": "0001-01-01T00:00:00Z",
	}
	if !reflect.DeepEqual(anns.StringMap(), expected) {
		t.Errorf("got %#v, want %#v", anns.StringMap(), expected)
	}
}

func TestMiddleware_useSpanFromHeaders(t *testing.T) {
	ms := apptrace.NewMemoryStore()
	c := apptrace.NewLocalCollector(ms)

	req, _ := http.NewRequest("GET", "http://example.com/foo", nil)
	req.Header.Set("X-Req-Header", "a")

	spanID := apptrace.SpanID{1, 2, 3}
	SetSpanIDHeader(req.Header, spanID)

	var setContextSpan apptrace.SpanID
	mw := Middleware(c, &MiddlewareConfig{
		RouteName:      func(r *http.Request) string { return "r" },
		CurrentUser:    func(r *http.Request) string { return "u" },
		SetContextSpan: func(r *http.Request, id apptrace.SpanID) { setContextSpan = id },
	})

	w := httptest.NewRecorder()
	mw(w, req, func(http.ResponseWriter, *http.Request) {})

	if setContextSpan != spanID {
		t.Errorf("set context span to %v, want %v", setContextSpan, spanID)
	}

	trace, err := ms.Trace(1)
	if err != nil {
		t.Fatal(err)
	}

	var e ServerEvent
	if err := apptrace.UnmarshalEvent(trace.Span.Annotations, &e); err != nil {
		t.Fatal(err)
	}

	wantEvent := ServerEvent{
		Request: RequestInfo{
			Method:  "GET",
			Proto:   "HTTP/1.1",
			URI:     "/foo",
			Host:    "example.com",
			Headers: map[string]string{"X-Req-Header": "a"},
		},
		Response: ResponseInfo{
			StatusCode: 200,
			Headers:    map[string]string{"Span-Id": "0000000000000001/0000000000000002/0000000000000003"},
		},
		User:  "u",
		Route: "r",
	}
	delete(e.Request.Headers, "Span-Id")
	e.ServerRecv = time.Time{}
	e.ServerSend = time.Time{}
	if !reflect.DeepEqual(e, wantEvent) {
		t.Errorf("got ServerEvent %+v, want %+v", e, wantEvent)
	}
}

func TestMiddleware_createNewSpan(t *testing.T) {
	ms := apptrace.NewMemoryStore()
	c := apptrace.NewLocalCollector(ms)

	req, _ := http.NewRequest("GET", "http://example.com/foo", nil)

	var setContextSpan apptrace.SpanID
	mw := Middleware(c, &MiddlewareConfig{
		SetContextSpan: func(r *http.Request, id apptrace.SpanID) { setContextSpan = id },
	})

	w := httptest.NewRecorder()
	mw(w, req, func(http.ResponseWriter, *http.Request) {})

	if setContextSpan == (apptrace.SpanID{0, 0, 0}) {
		t.Errorf("context span is zero, want it to be set")
	}

	trace, err := ms.Trace(setContextSpan.Trace)
	if err != nil {
		t.Fatal(err)
	}

	var e ServerEvent
	if err := apptrace.UnmarshalEvent(trace.Span.Annotations, &e); err != nil {
		t.Fatal(err)
	}

	wantEvent := ServerEvent{
		Request: RequestInfo{
			Method: "GET",
			Proto:  "HTTP/1.1",
			URI:    "/foo",
			Host:   "example.com",
		},
		Response: ResponseInfo{
			StatusCode: 200,
			Headers:    map[string]string{"Span-Id": setContextSpan.String()},
		},
	}
	delete(e.Request.Headers, "Span-Id")
	e.ServerRecv = time.Time{}
	e.ServerSend = time.Time{}
	if !reflect.DeepEqual(e, wantEvent) {
		t.Errorf("got ServerEvent %+v, want %+v", e, wantEvent)
	}
}

func TestServerEvent_unmarshal(t *testing.T) {
	m := map[string]string{
		"":                         "/foo",
		"_schema:name":             "",
		"User":                     "u",
		"ServerRecv":               "0001-01-01T00:00:00Z",
		"ServerSend":               "0001-01-01T00:00:00Z",
		"Request.Host":             "example.com",
		"Request.RemoteAddr":       "",
		"Request.ContentLength":    "0",
		"Request.Method":           "GET",
		"Request.URI":              "/foo",
		"Request.Proto":            "HTTP/1.1",
		"Response.Headers.Span-Id": "15409ac1437f45e4/118217713a143137",
		"Response.ContentLength":   "0",
		"Response.StatusCode":      "200",
		"Route":                    "r",
		"_schema:HTTPServer":       "",
	}
	var e ServerEvent
	if err := apptrace.UnmarshalEvent(mapToAnnotations(m), &e); err != nil {
		t.Fatal(err)
	}

	want := ServerEvent{
		Request: RequestInfo{
			Host:   "example.com",
			Method: "GET",
			URI:    "/foo",
			Proto:  "HTTP/1.1",
		},
		Response: ResponseInfo{
			Headers:    map[string]string{"Span-Id": "15409ac1437f45e4/118217713a143137"},
			StatusCode: 200,
		},
		Route: "r",
		User:  "u",
	}
	want.ServerRecv = want.ServerRecv.In(time.UTC)
	want.ServerSend = want.ServerSend.In(time.UTC)

	if !reflect.DeepEqual(e, want) {
		t.Errorf("got ServerEvent %+v, want %+v", e, want)
	}
}

func mapToAnnotations(m map[string]string) apptrace.Annotations {
	anns := make(apptrace.Annotations, 0, len(m))
	for k, v := range m {
		anns = append(anns, apptrace.Annotation{Key: k, Value: []byte(v)})
	}
	return anns
}
