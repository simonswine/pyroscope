package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"
)

type oneByteWriter struct{ w io.Writer }

func (w oneByteWriter) Write(p []byte) (int, error) { return w.w.Write(p[:1]) }

type oneByteReader struct{ r io.Reader }

func (r oneByteReader) Read(p []byte) (int, error) { return r.r.Read(p[:1]) }

func TestProtocolFramePartialIO(t *testing.T) {
	original := protocolFrame{Header: frameHeader{Version: protocolVersion, Kind: "chunk", StreamID: "blob", Offset: 7}, Payload: []byte("hello")}
	var buf bytes.Buffer
	if err := writeProtocolFrame(oneByteWriter{&buf}, original); err != nil {
		t.Fatal(err)
	}
	got, err := readProtocolFrame(oneByteReader{&buf})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Header, original.Header) || !bytes.Equal(got.Payload, original.Payload) {
		t.Fatalf("got %+v", got)
	}
}

func TestProtocolFrameRejectsInvalidInput(t *testing.T) {
	valid := protocolFrame{Header: frameHeader{Version: protocolVersion, Kind: "request", RequestID: "1"}}
	var buf bytes.Buffer
	if err := writeProtocolFrame(&buf, valid); err != nil {
		t.Fatal(err)
	}
	data := append([]byte(nil), buf.Bytes()...)
	for _, n := range []int{0, 3, 8, len(data) - 1} {
		_, err := readProtocolFrame(bytes.NewReader(data[:n]))
		if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			t.Fatalf("truncated %d: %v", n, err)
		}
	}
	oversized := append([]byte(nil), data...)
	binary.BigEndian.PutUint32(oversized[:4], maxFrameHeader+1)
	if _, err := readProtocolFrame(bytes.NewReader(oversized)); err == nil {
		t.Fatal("accepted oversized header")
	}
	binary.BigEndian.PutUint32(oversized[:4], binary.BigEndian.Uint32(data[:4]))
	binary.BigEndian.PutUint32(oversized[4:8], maxFramePayload+1)
	if _, err := readProtocolFrame(bytes.NewReader(oversized)); err == nil {
		t.Fatal("accepted oversized payload")
	}
	malformed := append([]byte(nil), data...)
	malformed[8] = '!'
	if _, err := readProtocolFrame(bytes.NewReader(malformed)); err == nil {
		t.Fatal("accepted invalid JSON")
	}
	valid.Header.Version = 2
	if err := writeProtocolFrame(&buf, valid); err == nil {
		t.Fatal("accepted unknown version")
	}
	valid.Header.Version = protocolVersion
	valid.Header.Kind = "chunk"
	valid.Header.StreamID = ""
	if err := writeProtocolFrame(&buf, valid); err == nil {
		t.Fatal("accepted missing stream")
	}
}
