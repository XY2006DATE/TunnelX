package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

const MaxDatagramSize = 65535

// ReadDatagram reads one length-prefixed UDP payload from a stream.
func ReadDatagram(r io.Reader, buffer []byte) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(header[:]))
	if size > len(buffer) {
		return nil, io.ErrShortBuffer
	}
	data := buffer[:size]
	_, err := io.ReadFull(r, data)
	return data, err
}

// WriteDatagram keeps packet boundaries when UDP travels over TCP or TLS.
func WriteDatagram(w io.Writer, data []byte) error {
	if len(data) > MaxDatagramSize {
		return fmt.Errorf("UDP payload too large: %d", len(data))
	}
	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(len(data)))
	if n, err := w.Write(header[:]); err != nil {
		return err
	} else if n != len(header) {
		return io.ErrShortWrite
	}
	if len(data) == 0 {
		return nil
	}
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
