package converter

import (
	"fmt"
	"strings"
)

// plural picks the Russian noun form for n: one (1, 21), few (2-4, 22-24) or
// many (0, 5-20, 25-30...). A copy of the docs adapter's plural, forced by
// layering: the converter cannot import the telegram package.
func plural(n int, one, few, many string) string {
	if n < 0 {
		n = -n
	}
	switch {
	case n%100 >= 11 && n%100 <= 14:
		return many
	case n%10 == 1:
		return one
	case n%10 >= 2 && n%10 <= 4:
		return few
	default:
		return many
	}
}

// humanSize formats a byte count in KB or MB; "" when the size is unknown. A
// copy of the docs adapter's humanSize, forced by layering.
func humanSize(n int64) string {
	if n <= 0 {
		return ""
	}
	if kb := (n + 1023) / 1024; kb < 1024 {
		return fmt.Sprintf(textSizeKB, kb)
	}
	return strings.ReplaceAll(fmt.Sprintf("%.1f", float64(n)/(1<<20)), ".", ",") + textSizeMBSuffix
}
