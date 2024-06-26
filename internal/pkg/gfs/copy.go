package gfs

import (
	"context"
	"io"
)

func genericCopy(ctx context.Context, dest io.Writer, src io.Reader, buf []byte) error {
	_, err := io.CopyBuffer(dest, NewReader(ctx, src), buf)

	return err
}
