package proto

import (
	"encoding/binary"
	"io"
)

type ChunkRequest struct {
	PieceIndex uint32
	Begin      uint32
	Length     uint32
}

func SendRequest(conn io.Writer, request ChunkRequest) error {
	return sendRequestPayload(conn, Request, request)
}

func SendCancel(conn io.Writer, request ChunkRequest) error {
	return sendRequestPayload(conn, Cancel, request)
}

func SendReject(conn io.Writer, request ChunkRequest) error {
	return sendRequestPayload(conn, Reject, request)
}

func sendRequestPayload(conn io.Writer, id Message, request ChunkRequest) error {
	var b [sizeUint32 + sizeByte + sizeUint32*3]byte

	binary.BigEndian.PutUint32(b[:], sizeByte+sizeUint32*3)
	b[4] = byte(id)
	binary.BigEndian.PutUint32(b[sizeUint32+sizeByte:], request.PieceIndex)
	binary.BigEndian.PutUint32(b[sizeUint32+sizeByte+sizeUint32:], request.Begin)
	binary.BigEndian.PutUint32(b[sizeUint32+sizeByte+sizeUint32+sizeUint32:], request.Length)

	_, err := conn.Write(b[:])
	return err
}

func ReadRequestPayload(conn io.Reader) (payload ChunkRequest, err error) {
	var b [sizeUint32 * 3]byte

	_, err = io.ReadFull(conn, b[:])
	if err != nil {
		return
	}

	payload.PieceIndex = binary.BigEndian.Uint32(b[:])
	payload.Begin = binary.BigEndian.Uint32(b[sizeUint32:])
	payload.Length = binary.BigEndian.Uint32(b[sizeUint32*2:])

	return
}
