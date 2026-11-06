package protocol

import (
	"bytes"
	"io"
	"testing"
	"testing/iotest"
)

func TestDatagramsSurviveFragmentedStream(t *testing.T) {
	want := [][]byte{[]byte("first"), nil, bytes.Repeat([]byte{0, 0xff}, 30000), []byte("last")}
	var stream bytes.Buffer
	for _, packet := range want {
		if err := WriteDatagram(&stream, packet); err != nil {
			t.Fatal(err)
		}
	}
	fragmented := iotest.OneByteReader(&stream)
	buffer := make([]byte, MaxDatagramSize)
	for i, packet := range want {
		got, err := ReadDatagram(fragmented, buffer)
		if err != nil || !bytes.Equal(got, packet) {
			t.Fatalf("packet %d: got %d bytes, want %d bytes, error %v", i, len(got), len(packet), err)
		}
	}
	if _, err := ReadDatagram(fragmented, buffer); err != io.EOF {
		t.Fatalf("end of stream: %v", err)
	}
}
