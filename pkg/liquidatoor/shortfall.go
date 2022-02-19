package liquidatoor

type ByShortfall []Borrower

func (a ByShortfall) Len() int           { return len(a) }
func (a ByShortfall) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByShortfall) Less(i, j int) bool { return a[i].Shortfall.Cmp(a[j].Shortfall) == 1 }
