//go:build ignore

// gen32 converts a compiled terminfo entry in the legacy format (magic 0432,
// 16-bit numbers) to the extended number format (magic 01036, 32-bit
// numbers), the way ncurses 6.1+ tic writes it. It exists because the tic on
// the machine that generated these fixtures (ncurses 6.0) cannot write that
// format.
//
//	go run testdata/gen32.go IN OUT
package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: go run gen32.go IN OUT")
		os.Exit(2)
	}
	in, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail(err)
	}
	out, err := convert(in)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(os.Args[2], out, 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func convert(b []byte) ([]byte, error) {
	le := binary.LittleEndian
	u16 := func(off int) int { return int(int16(le.Uint16(b[off:]))) }
	if le.Uint16(b) != 0o432 {
		return nil, fmt.Errorf("not a legacy terminfo entry")
	}
	var out []byte
	put16 := func(v int) { out = le.AppendUint16(out, uint16(int16(v))) }
	put32 := func(v int) { out = le.AppendUint32(out, uint32(int32(v))) }
	copyN := func(off, n int) int { out = append(out, b[off:off+n]...); return off + n }
	align := func(off int) int {
		if off%2 == 1 && off < len(b) {
			out = append(out, b[off])
			off++
		}
		return off
	}
	numbers := func(off, n int) int {
		for i := 0; i < n; i++ {
			put32(u16(off))
			off += 2
		}
		return off
	}

	nameSize, boolCount, numCount, strCount, strSize := u16(2), u16(4), u16(6), u16(8), u16(10)
	put16(0o1036)
	for _, v := range []int{nameSize, boolCount, numCount, strCount, strSize} {
		put16(v)
	}
	off := 12
	off = copyN(off, nameSize+boolCount)
	off = align(off)
	off = numbers(off, numCount)
	off = copyN(off, strCount*2+strSize)
	off = align(off)
	if len(b)-off < 10 {
		return append(out, b[off:]...), nil
	}
	extBool, extNum, extStr, extItems, extSize := u16(off), u16(off+2), u16(off+4), u16(off+6), u16(off+8)
	off = copyN(off, 10)
	off = copyN(off, extBool)
	off = align(off)
	off = numbers(off, extNum)
	_ = extItems
	off = copyN(off, (extStr+extBool+extNum+extStr)*2+extSize)
	return append(out, b[off:]...), nil
}
