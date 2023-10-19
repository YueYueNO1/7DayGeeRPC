package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

// 定义gob的结构体。由四部分组成
type GobCodec struct {
	conn io.ReadWriteCloser //由构建函数传入，通常是通过tcp或unix建立socket时得到的链接实例
	buf  *bufio.Writer      //防止阻塞而创建的带缓冲的Writer，一般这么做是为了提高性能
	dec  *gob.Decoder       //对应gob的decoder
	enc  *gob.Encoder       //对应gob的encoder
}

var _ Codec = (*GobCodec)(nil)

func NewGobCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf:  buf,
		dec:  gob.NewDecoder(conn),
		enc:  gob.NewEncoder(buf),
	}
}

func (c *GobCodec) ReadHeader(h *Header) error {
	return c.dec.Decode(h)
}

func (c *GobCodec) ReadBody(body interface{}) error {
	return c.dec.Decode(body)
}

func (c *GobCodec) Write(h *Header, body interface{}) (err error) {
	defer func() {
		_ = c.buf.Flush()
		if err != nil {
			_ = c.Close()
		}
	}()
	if err = c.enc.Encode(h); err != nil {
		log.Println("rpc: gob error encoding header:", err)
		return
	}
	if err = c.enc.Encode(body); err != nil {
		log.Println("rpc: gob error encoding body:", err)
		return
	}
	return
}

func (c *GobCodec) Close() error {
	return c.conn.Close()
}
