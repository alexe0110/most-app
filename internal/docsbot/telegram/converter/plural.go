package converter

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
