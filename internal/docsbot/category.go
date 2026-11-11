package docsbot

// Category is a document bucket. The stored value is a stable slug; the Russian
// label (also the Drive folder name) comes from the categories table below.
type Category string

// Categories (product.md).
const (
	CatPassport     Category = "passport"
	CatCertificates Category = "certificates"
	CatInsurance    Category = "insurance"
	CatRealty       Category = "realty"
	CatReceipts     Category = "receipts"
	CatAuto         Category = "auto"
	CatMedical      Category = "medical"
	CatTravel       Category = "travel"
	CatEducation    Category = "education"
	CatWork         Category = "work"
	CatOther        Category = "other"
)

// categories is the single source of truth: slug, display label and Drive folder
// name, in display order. CatOther must stay last.
var categories = []struct {
	slug  Category
	label string
}{
	{CatPassport, "Паспорт и ID"},
	{CatCertificates, "Справки"},
	{CatInsurance, "Страховки"},
	{CatRealty, "Недвижимость"},
	{CatReceipts, "Чеки и гарантии"},
	{CatAuto, "Авто"},
	{CatMedical, "Медицина"},
	{CatTravel, "Путешествия"},
	{CatEducation, "Учёба"},
	{CatWork, "Работа"},
	{CatOther, "Другое"},
}

// AllCategories lists the categories in display order (product.md).
var AllCategories = func() []Category {
	out := make([]Category, len(categories))
	for i, c := range categories {
		out[i] = c.slug
	}
	return out
}()

// Valid reports whether c is one of the known categories.
func (c Category) Valid() bool {
	for _, k := range categories {
		if c == k.slug {
			return true
		}
	}
	return false
}

// Label is the Russian display name. An unknown or retired slug reads «Другое».
func (c Category) Label() string {
	for _, k := range categories {
		if c == k.slug {
			return k.label
		}
	}
	return CatOther.Label()
}

// FolderName is the Drive folder name of the category; it equals the label.
func (c Category) FolderName() string { return c.Label() }

// ParseCategory turns a stored or callback string into a Category.
func ParseCategory(s string) (Category, bool) {
	c := Category(s)
	return c, c.Valid()
}

// foldCategoryCounts merges counts of unknown (retired) slugs into CatOther and
// orders the result by AllCategories; categories without documents are omitted.
func foldCategoryCounts(counts []CategoryCount) []CategoryCount {
	byCat := make(map[Category]int, len(AllCategories))
	for _, cc := range counts {
		c := cc.Category
		if !c.Valid() {
			c = CatOther
		}
		byCat[c] += cc.Count
	}
	var out []CategoryCount
	for _, c := range AllCategories {
		if n := byCat[c]; n > 0 {
			out = append(out, CategoryCount{Category: c, Count: n})
		}
	}
	return out
}
