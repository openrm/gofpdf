/*
 * Copyright (c) 2013-2016 Kurt Jung (Gmail: kurt.w.jung)
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package gofpdf

import (
	"io"
	"fmt"
	"bytes"
	"strings"
	"compress/zlib"
	"encoding/binary"
)

const pngSignature = "\x89PNG\x0d\x0a\x1a\x0a"

const (
	stateInit = iota
	stateSignature
	stateHeader
	stateAnc
	stateData
	stateEnd
)

func min(a, b int) int {
 if a < b {
  return a
 }
 return b
}

type pngStream struct {
	r io.Reader
	state int
	readable uint32
	available int
	buf [3 << 8]byte
	tmp [2 << 10]byte
	off int
	trns []int
	pal []byte
	readdpi bool
	dpi float64
	w, h uint32
	bpc, ct byte
}

func (p *pngStream) next(n int) (int, error) {
	n, err := io.ReadFull(p.r, p.buf[:n])
	if err == io.ErrUnexpectedEOF {
		return 0, io.EOF
	}
	return n, err
}

func (p *pngStream) readByte() (byte, error) {
	if _, err := p.next(1); err != nil {
		return 0, err
	} else {
		return p.buf[0], nil
	}
}

func (p *pngStream) readBeUint32() (uint32, error) {
	if n, err := p.next(4); err != nil {
		return 0, err
	} else {
		return binary.BigEndian.Uint32(p.buf[:n]), nil
	}
}

func (p *pngStream) readString(n int) (string, error) {
	if n, err := p.next(n); err != nil {
		return "", err
	} else {
		return string(p.buf[:n]), nil
	}
}

func (p *pngStream) parseSignature() error {
	p.state = stateSignature
	if sign, err := p.readString(len(pngSignature)); err != nil {
		return err
	} else if sign != pngSignature {
		return fmt.Errorf("not a PNG buffer")
	}
	return nil
}

func (p *pngStream) parseHeader() (err error) {
	if p.w, err = p.readBeUint32(); err != nil {
		return
	}
	if p.h, err = p.readBeUint32(); err != nil {
		return
	}
	if p.bpc, err = p.readByte(); err != nil {
		return
	}
	if p.bpc > 8 {
		return fmt.Errorf("16-bit depth not supported in PNG file")
	}
	if p.ct, err = p.readByte(); err != nil {
		return
	}
	var b byte
	if b, err = p.readByte(); err != nil {
		return
	} else if b != 0 {
		return fmt.Errorf("'unknown compression method in PNG buffer")
	}
	if b, err = p.readByte(); err != nil {
		return
	} else if b != 0 {
		return fmt.Errorf("'unknown filter method in PNG buffer")
	}
	if b, err = p.readByte(); err != nil {
		return
	} else if b != 0 {
		return fmt.Errorf("interlacing not supported in PNG buffer")
	}
	return nil
}

func (p *pngStream) parsePalette() error {
	p.pal = make([]byte, p.readable)
	_, err := io.ReadFull(p.r, p.pal)
	return err
}

func (p *pngStream) parsetRNS() error {
	t := make([]byte, p.readable)
	if _, err := io.ReadFull(p.r, t); err != nil {
		return err
	}
	switch p.ct {
	case 0:
		p.trns = []int{int(t[1])} // ord(substr($t,1,1)));
	case 2:
		p.trns = []int{int(t[1]), int(t[3]), int(t[5])} // array(ord(substr($t,1,1)), ord(substr($t,3,1)), ord(substr($t,5,1)));
	default:
		pos := strings.Index(string(t), "\x00")
		if pos >= 0 {
			p.trns = []int{pos} // array($pos);
		}
	}
	return nil
}

func (p *pngStream) parsepHYs() error {
	// png files theoretically support different x/y dpi
	// but we ignore files like this
	// but if they're the same then we can stamp our info
	// object with it
	var (
		err error
		x, y uint32
		units byte
	)
	if x, err = p.readBeUint32(); err != nil {
		return err
	}
	if y, err = p.readBeUint32(); err != nil {
		return err
	}
	if units, err = p.readByte(); err != nil {
		return err
	}
	// fmt.Printf("got a pHYs block, x=%d, y=%d, u=%d, readdpi=%t\n",
	// x, y, int(units), readdpi)
	// only modify the info block if the user wants us to
	if x == y && p.readdpi {
		switch units {
			// if units is 1 then measurement is px/meter
		case 1:
			p.dpi = float64(x) / 39.3701 // inches per meter
		default:
			p.dpi = float64(x)
		}
	}
	return nil
}

func (p *pngStream) ignoreChunk(n int) error {
	for n > 0 {
		if m, err := io.ReadFull(p.r, p.buf[:min(n, len(p.buf))]); err != nil {
			return err
		} else {
			n -= m
		}
	}
	return nil
}

func (p *pngStream) parseChunk() (err error) {
	var ct string
	if p.readable, err = p.readBeUint32(); err != nil {
		return
	}
	if ct, err = p.readString(4); err != nil {
		return
	}
	switch ct {
	case "IHDR":
		// Read header chunk
		p.state = stateHeader
		err = p.parseHeader()
	case "PLTE":
		p.state = stateAnc
		err = p.parsePalette()
	case "tRNS":
		p.state = stateAnc
		err = p.parsetRNS()
	case "pHYs":
		p.state = stateAnc
		err = p.parsepHYs()
	case "IDAT":
		p.state = stateData
		return
	case "IEND":
		p.state = stateEnd
	default:
		err = p.ignoreChunk(int(p.readable))
	}
	if err != nil {
		return
	}
	// ignore CRC
	_, err = p.next(4)
	return
}

func (p *pngStream) parseUntil(state int) error {
	if p.state == stateInit {
		// 	Check signature
		if err := p.parseSignature(); err != nil {
			return err
		}
	}
	for p.state < state {
		if err := p.parseChunk(); err != nil {
			return err
		}
	}
	return nil
}

func (p *pngStream) Read(buf []byte) (int, error) {
	if p.state > stateData {
		return 0, io.EOF
	}
	if err := p.parseUntil(stateData); err != nil {
		return 0, err
	}
	if p.readable < 0 {
		return 0, fmt.Errorf("IDAT chunk length overflow")
	}
	for p.readable == 0 {
		if err := p.parseChunk(); err != nil {
			return 0, err
		}
	}
	n, err := p.r.Read(buf[:min(len(buf), int(p.readable))])
	p.readable -= uint32(n)
	if p.readable <= 0 {
		_, err := p.next(4)
		return n, err
	}
	return n, err
}

func (f *Fpdf) pngColorSpace(ct byte) (colspace string, colorVal int) {
	colorVal = 1
	switch ct {
	case 0, 4:
		colspace = "DeviceGray"
	case 2, 6:
		colspace = "DeviceRGB"
		colorVal = 3
	case 3:
		colspace = "Indexed"
	default:
		f.err = fmt.Errorf("unknown color type in PNG buffer: %d", ct)
	}
	return
}

type alphaSeparator struct {
	rc io.Reader
	w, h, chs, stride int
	alpha *bytes.Buffer
	writer *zlib.Writer
	off int
}

func newAlphaSeparator(rc io.Reader, w, h, chs int, buf *bytes.Buffer) *alphaSeparator {
	writer, _ := zlib.NewWriterLevel(buf, zlib.BestSpeed)
	return &alphaSeparator{
		rc: rc,
		w: w,
		h: h,
		chs: chs,
		stride: 1 + (chs + 1) * w,
		alpha: buf,
		writer: writer,
	}
}

func (a *alphaSeparator) readPaletteIndex(buf []byte) (int, error) {
	if n, err := io.ReadFull(a.rc, buf[:1]); err != nil {
		return 0, err
	} else {
		a.off += n
		_, err = a.writer.Write(buf[:1])
		return n, err
	}
}

func (a *alphaSeparator) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	if a.off >= a.h * a.stride {
		return 0, io.EOF
	}
	j := a.off % a.stride
	// i, j := a.off / a.stride, a.off % a.stride
	if j == 0 {
		return a.readPaletteIndex(buf)
	} else {
		c := (j - 1) % (a.chs + 1)
		// x, y, c := j / (a.chs + 1), i, (j - 1) % (a.chs + 1)
		if n, err := io.ReadFull(a.rc, buf[:a.chs + 1 - c]); err != nil {
			a.off += n
			return n, err
		} else {
			a.off += n
			_, err = a.writer.Write(buf[a.chs - c:a.chs + 1 - c])
			return n - 1, err
		}
	}
}

func (a *alphaSeparator) Flush() error {
	return a.writer.Flush()
}

func (a *alphaSeparator) Close() error {
	return a.writer.Close()
}

func (f *Fpdf) parsepngstream(r io.Reader, readdpi bool) (info *ImageInfoType) {
	p := &pngStream{r: r, readdpi: readdpi}
	if err := p.parseUntil(stateData); err != nil {
		f.err = err
		return
	}
	var colspace string
	var colorVal int
	colspace, colorVal = f.pngColorSpace(p.ct)
	if f.err != nil {
		return
	}
	if colspace == "Indexed" && len(p.pal) == 0 {
		f.err = fmt.Errorf("missing palette in PNG buffer")
	}
	info = f.newImageInfo()
	info.w = float64(p.w)
	info.h = float64(p.h)
	info.cs = colspace
	info.bpc = int(p.bpc)
	info.f = "FlateDecode"
	info.dp = sprintf("/Predictor 15 /Colors %d /BitsPerComponent %d /Columns %d", colorVal, p.bpc, p.w)
	info.pal = p.pal
	info.trns = p.trns
	info.r = p
	if p.ct >= 4 {
		stm, err := zlib.NewReader(p)
		if err != nil {
			f.err = err
			return
		}
		var chs int
		if p.ct == 4 {
			chs = 1
		} else {
			chs = 3
		}
		astm := newAlphaSeparator(stm, int(info.w), int(info.h), chs, new(bytes.Buffer))
		cstm := newCompressor(astm)
		info.r = cstm
		info.smask = astm.alpha
		info.flush = astm.Flush
		info.addCloseHook(stm.Close, astm.Close, cstm.Close)
		if f.pdfVersion < "1.4" {
			f.pdfVersion = "1.4"
		}
	}
	return
}
