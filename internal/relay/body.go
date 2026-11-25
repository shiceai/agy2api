package relay

import "io"

// cancelReadCloser 在 Close 时额外调用 cancel，用于回收流式请求的 context。
type cancelReadCloser struct {
	io.ReadCloser
	cancel func()
}

func (c cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	if c.cancel != nil {
		c.cancel()
	}
	return err
}

func withCancel(body io.ReadCloser, cancel func()) io.ReadCloser {
	return cancelReadCloser{ReadCloser: body, cancel: cancel}
}
