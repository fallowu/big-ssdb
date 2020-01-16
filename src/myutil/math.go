package myutil

import (
	"fmt"
	"strconv"
)

func Atoi(s string) int{
	n, _ := strconv.Atoi(s)
	return n
}

func Atou(s string) uint32{
	n, _ := strconv.ParseUint(s, 10, 32)
	return uint32(n)
}

func Utoa(u uint32) string{
	return fmt.Sprintf("%d", u)
}

func Atou64(s string) uint64{
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

func Utoa64(u uint64) string{
	return fmt.Sprintf("%d", u)
}

func MinInt(a, b int) int{
	if a < b {
		return a
	} else {
		return b
	}
}

func MinU64(a, b uint64) uint64{
	if a < b {
		return a
	} else {
		return b
	}
}

func MaxU64(a, b uint64) uint64{
	if a > b {
		return a
	} else {
		return b
	}
}
