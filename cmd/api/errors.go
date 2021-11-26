package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var (
	ErrBadURLQuery = errors.New("bad url query")
	ErrInternal    = &StatusError{
		Err:    stringErr("Internal Server Error"),
		Status: 500,
		Reason: "something went wrong on the server side",
	}
)

func wrapstatus(err error, status int, reason ...string) error {
	return &StatusError{
		Err:    err,
		Status: status,
		Reason: strings.Join(reason, ", "),
	}
}

func internal(err error) error {
	return &InternalError{Err: err}
}

func fail(w http.ResponseWriter, errs ...error) {
	var err error
	if len(errs) == 0 {
		err = ErrInternal
	} else {
		err = errs[0]
	}

	if resp, ok := w.(*response); ok {
		resp.err = err // for logging after request is made
	} else {
		log.Error(err)
	}

	switch e := err.(type) {
	case *StatusError:
		w.WriteHeader(e.Status)
	case *InternalError:
		w.WriteHeader(500)
	default:
		w.WriteHeader(500)
	}
	if err = sendJSONError(w, err); err != nil {
		log.WithError(err).Error("could not write json error to response")
	}
}

func sendJSONError(w io.Writer, err error) error {
	if e, ok := err.(json.Marshaler); ok {
		b, err := e.MarshalJSON()
		if err != nil {
			log.WithError(err).Error("could not write json error to response")
			return err
		}
		if _, err = w.Write(b); err != nil {
			log.WithError(err).Error("could not write json error to response")
			return err
		}
	} else {
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
	}
	return nil
}

type StatusError struct {
	Err    error  `json:"error"`
	Reason string `json:"reason,omitempty"`
	Status int    `json:"status,omitempty"`
}

func (se *StatusError) Error() string {
	return se.Err.Error()
}

func (se *StatusError) Unwrap() error {
	return se.Err
}

func (se *StatusError) WriteTo(w io.Writer) (int64, error) {
	type e struct {
		Err    string `json:"error"`
		Reason string `json:"reason,omitempty"`
		Status int    `json:"status,omitempty"`
	}

	b, err := json.Marshal(&e{
		Err:    se.Err.Error(),
		Reason: se.Reason,
		Status: se.Status,
	})
	if err != nil {
		return 0, err
	}
	n, err := w.Write(b)
	return int64(n), err
}

func (se *StatusError) MarshalJSON() ([]byte, error) {
	type e struct {
		Err    string `json:"error"`
		Reason string `json:"reason,omitempty"`
		Status int    `json:"status,omitempty"`
	}
	return json.Marshal(&e{
		Err:    se.Err.Error(),
		Reason: se.Reason,
		Status: se.Status,
	})
}

type InternalError struct {
	Err    error
	Status int
}

func (ie *InternalError) Error() string {
	return ie.Err.Error()
}

func (ie *InternalError) Unwrap() error {
	return ie.Err
}

func (ie *InternalError) MarshalJSON() ([]byte, error) {
	const msg = `{"error":"Internal Server Error","reason":"something went wrong on the server side","status":500}`
	return []byte(msg), nil
}

var _ json.Marshaler = (*StatusError)(nil)

type stringErr string

func (s stringErr) Error() string { return string(s) }
