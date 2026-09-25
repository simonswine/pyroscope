package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	protocolVersion = 1
	maxFrameHeader  = 64 << 10
	maxFramePayload = 1 << 20
)

type protocolError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type frameHeader struct {
	Version   int             `json:"version"`
	Kind      string          `json:"kind"`
	RequestID string          `json:"request_id,omitempty"`
	Method    string          `json:"method,omitempty"`
	RunID     string          `json:"run_id,omitempty"`
	StreamID  string          `json:"stream_id,omitempty"`
	Offset    int64           `json:"offset,omitempty"`
	Sequence  uint64          `json:"sequence,omitempty"`
	Body      json.RawMessage `json:"body,omitempty"`
	Error     *protocolError  `json:"error,omitempty"`
}

type protocolFrame struct {
	Header  frameHeader
	Payload []byte
}

func validFrameHeader(h frameHeader, payloadSize int) error {
	if h.Version != protocolVersion {
		return fmt.Errorf("unsupported protocol version %d", h.Version)
	}
	switch h.Kind {
	case "request", "response", "event", "chunk", "credit", "cancel":
	default:
		return fmt.Errorf("invalid frame kind %q", h.Kind)
	}
	if h.Offset < 0 {
		return errors.New("negative frame offset")
	}
	if h.Kind == "chunk" && h.StreamID == "" {
		return errors.New("chunk without stream ID")
	}
	if h.Kind != "chunk" && h.Kind != "response" && payloadSize != 0 {
		return errors.New("unexpected frame payload")
	}
	if (h.Kind == "request" || h.Kind == "response" || h.Kind == "cancel") && h.RequestID == "" {
		return errors.New("missing request ID")
	}
	return nil
}

func readProtocolFrame(r io.Reader) (protocolFrame, error) {
	var lengths [8]byte
	if _, err := io.ReadFull(r, lengths[:]); err != nil {
		return protocolFrame{}, err
	}
	headerSize := binary.BigEndian.Uint32(lengths[:4])
	payloadSize := binary.BigEndian.Uint32(lengths[4:])
	if headerSize == 0 || headerSize > maxFrameHeader || payloadSize > maxFramePayload {
		return protocolFrame{}, errors.New("protocol frame exceeds size limits")
	}
	headerBytes := make([]byte, headerSize)
	if _, err := io.ReadFull(r, headerBytes); err != nil {
		return protocolFrame{}, err
	}
	var h frameHeader
	if err := json.Unmarshal(headerBytes, &h); err != nil {
		return protocolFrame{}, fmt.Errorf("invalid frame header: %w", err)
	}
	if err := validFrameHeader(h, int(payloadSize)); err != nil {
		return protocolFrame{}, err
	}
	payload := make([]byte, payloadSize)
	if _, err := io.ReadFull(r, payload); err != nil {
		return protocolFrame{}, err
	}
	return protocolFrame{Header: h, Payload: payload}, nil
}

func writeProtocolFrame(w io.Writer, f protocolFrame) error {
	if len(f.Payload) > maxFramePayload {
		return errors.New("protocol payload exceeds size limit")
	}
	if err := validFrameHeader(f.Header, len(f.Payload)); err != nil {
		return err
	}
	header, err := json.Marshal(f.Header)
	if err != nil {
		return err
	}
	if len(header) > maxFrameHeader {
		return errors.New("protocol header exceeds size limit")
	}
	var lengths [8]byte
	binary.BigEndian.PutUint32(lengths[:4], uint32(len(header)))
	binary.BigEndian.PutUint32(lengths[4:], uint32(len(f.Payload)))
	for _, part := range [][]byte{lengths[:], header, f.Payload} {
		for len(part) > 0 {
			n, err := w.Write(part)
			if n < 0 || n > len(part) {
				return errors.New("invalid protocol write count")
			}
			part = part[n:]
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
		}
	}
	return nil
}
