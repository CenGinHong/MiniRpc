package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

type GobCodec struct {
	conn io.ReadCloser // 链接实例
	buf  *bufio.Writer // 带缓冲的writer
	dec  *gob.Decoder  // 解码器
	enc  *gob.Encoder  // 编码器
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

func (g *GobCodec) Close() error {
	return g.conn.Close()
}

func (g *GobCodec) ReadHeader(header *Header) error {
	return g.dec.Decode(header)
}

func (g *GobCodec) ReadBody(body interface{}) error {
	return g.dec.Decode(body)
}

func (g *GobCodec) Write(header *Header, body interface{}) (err error) {
	defer func() {
		// 注意这里，因为写是连续的，这里仅当在write时出现错误才去close
		if err != nil {
			if err = g.Close(); err != nil {
				log.Println("rpc codec: gob error closing:", err)
			}
		}
	}()
	defer func() {
		if err = g.buf.Flush(); err != nil {
			log.Println("rpc codec: gob error flushing:", err)
		}
	}()
	// header编码并写入conn
	if err = g.enc.Encode(header); err != nil {
		log.Println("rpc codec: gob error encoding header:", err)
		return err
	}
	// body编码并写入conn
	if err = g.enc.Encode(body); err != nil {
		log.Println("rpc codec: gob error encoding body:", err)
		return err
	}
	return nil
}

// go的技巧，确认Codec是否实现了GobCodec接口，如果没有则会编译不过
var _ Codec = (*GobCodec)(nil)
