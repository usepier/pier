package pier

import (
	"fmt"
	"math/rand/v2"
)

// standIns are the names a new session can stand in with until its first
// prompt names it: short, easy to say, unlike any branch a person would
// make.
var standIns = []string{
	"accra", "amman", "antwerp", "athens", "austin", "baku", "bali", "bergen", "bern", "bilbao",
	"bogota", "bonn", "boston", "bremen", "brno", "cairo", "cali", "cork", "cusco", "dakar",
	"delft", "denver", "derby", "dhaka", "dublin", "durban", "essen", "faro", "fez", "galway",
	"gdansk", "genoa", "ghent", "giza", "graz", "hanoi", "havana", "hilo", "izmir", "jaipur",
	"juneau", "kabul", "kiev", "kobe", "kyoto", "lagos", "lahore", "leeds", "leipzig", "lima",
	"linz", "lisbon", "lyon", "macau", "madrid", "malmo", "manila", "medan", "milan", "minsk",
	"mumbai", "munich", "nairobi", "nantes", "naples", "nice", "oaxaca", "odessa", "osaka", "oslo",
	"ottawa", "oulu", "padua", "paris", "perth", "porto", "prague", "quito", "riga", "rome",
	"sapporo", "seoul", "seville", "sofia", "split", "suva", "sydney", "tallinn", "tampa", "tokyo",
	"toledo", "tunis", "turin", "turku", "utrecht", "valencia", "venice", "vienna", "vilnius", "zagreb",
}

// SuggestName is a stand-in name for a new session that no session (and
// no branch the caller knows of) has.
func SuggestName(taken func(string) bool) string {
	for _, i := range rand.Perm(len(standIns)) {
		if !taken(standIns[i]) {
			return standIns[i]
		}
	}
	for n := 2; ; n++ {
		name := fmt.Sprintf("%s-%d", standIns[rand.IntN(len(standIns))], n)
		if !taken(name) {
			return name
		}
	}
}
