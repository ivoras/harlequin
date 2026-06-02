// Command gen produces the memory-slot evaluation dataset: 1000 shared-scope
// memories with (content, key, value) structure and a set of sloppy,
// non-technical user queries with known target memory ids. Output is written as
// JSON under eval/memslot/data for reuse. Deterministic (fixed seed).
//
//	go run ./eval/memslot/gen
package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
)

// Memory mirrors the eval dataset record (a shared-scope memory + its slot).
type Memory struct {
	ID      string `json:"id"`
	Scope   string `json:"scope"`
	Content string `json:"content"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

// Query is one sloppy user query with the memory id(s) that answer it.
type Query struct {
	ID        string   `json:"id"`
	Query     string   `json:"query"`
	TargetIDs []string `json:"target_ids"`
	Key       string   `json:"key"`
}

var rng = rand.New(rand.NewSource(42))

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.':
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func pick(xs []string) string { return xs[rng.Intn(len(xs))] }

var (
	productAdj  = []string{"Nimbus", "Quantum", "Aero", "Lumen", "Vortex", "Pulse", "Zephyr", "Apex", "Cobalt", "Nova", "Falcon", "Orbit", "Titan", "Echo", "Solstice", "Halcyon"}
	productNoun = []string{"Headphones", "Speaker", "Router", "Keyboard", "Monitor", "Webcam", "Dock", "Drive", "Charger", "Lamp", "Thermostat", "Camera", "Tablet", "Earbuds", "Projector", "Microphone"}
	firstNames  = []string{"Jane", "Omar", "Mei", "Liam", "Sofia", "Noah", "Aisha", "Lucas", "Priya", "Ethan", "Hana", "Diego", "Yara", "Felix", "Ingrid", "Marco", "Nadia", "Tariq", "Elena", "Sven", "Rosa", "Kofi", "Lena", "Pablo", "Greta", "Hugo", "Maya", "Arjun", "Clara", "Ivan"}
	lastNames   = []string{"Doe", "Khan", "Lin", "Murphy", "Reyes", "Schmidt", "Okafor", "Bianchi", "Patel", "Nguyen", "Kowalski", "Santos", "Haddad", "Andersen", "Costa", "Volkov", "Mbeki", "Rossi", "Larsson", "Park", "Silva", "Fischer", "Nakamura", "Owusu", "Dubois", "Moreau", "Singh", "Weber", "Ferrari", "Petrov"}
	cities      = []string{"Berlin", "Toronto", "Singapore", "Austin", "Dublin", "Sydney", "Tokyo", "Lisbon"}
	vendors     = []string{"Globex Supplies", "Initech Logistics", "Soylent Foods", "Hooli Cloud", "Umbrella Components", "Stark Materials", "Wayne Freight"}
	months      = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	tz          = map[string]string{"Berlin": "CET", "Toronto": "Eastern", "Singapore": "SGT", "Austin": "Central", "Dublin": "GMT", "Sydney": "AEST", "Tokyo": "JST", "Lisbon": "WET"}
)

func date() string { return fmt.Sprintf("%s %d, %d", pick(months), 1+rng.Intn(28), 2019+rng.Intn(7)) }

// attr defines a memory attribute: how to build its value, its content sentence,
// and the sloppy query phrasings (with {name}).
type attr struct {
	name    string
	value   func(name, sl string) string
	content string // template with {name} and {value}
	queries []string
}

var n = 0

func mkID() string { n++; return fmt.Sprintf("m%04d", n) }

func gen(prefix, name string, attrs []attr, mems *[]Memory, queryPool *[]Query, qPerEntity int) {
	sl := slug(name)
	// instantiate every attribute as a memory
	idx := map[string]Memory{}
	for _, a := range attrs {
		v := a.value(name, sl)
		m := Memory{
			ID:      mkID(),
			Scope:   "shared",
			Key:     prefix + "." + sl + "." + a.name,
			Value:   v,
			Content: strings.NewReplacer("{name}", name, "{value}", v).Replace(a.content),
		}
		*mems = append(*mems, m)
		idx[a.name] = m
	}
	// emit a few sloppy queries for randomly chosen attributes of this entity
	used := map[string]bool{}
	for i := 0; i < qPerEntity; i++ {
		a := attrs[rng.Intn(len(attrs))]
		if used[a.name] {
			continue
		}
		used[a.name] = true
		m := idx[a.name]
		q := strings.ReplaceAll(pick(a.queries), "{name}", sloppyName(name))
		*queryPool = append(*queryPool, Query{Query: q, TargetIDs: []string{m.ID}, Key: m.Key})
	}
}

// sloppyName lowercases the entity name and occasionally drops a leading word,
// mimicking how distracted users type ("nimbus headphones" -> "headphones").
func sloppyName(name string) string {
	low := strings.ToLower(name)
	if parts := strings.Fields(low); len(parts) > 1 && rng.Intn(4) == 0 {
		return strings.Join(parts[1:], " ")
	}
	return low
}

func main() {
	var mems []Memory
	var queries []Query

	productAttrs := []attr{
		{"price", func(_, _ string) string { return fmt.Sprintf("$%d.99", 10+rng.Intn(490)) },
			"The {name} is priced at {value}.",
			[]string{"how much is the {name}", "whats the price of {name}", "{name} cost", "price on {name}", "how much does {name} run"}},
		{"release_date", func(_, _ string) string { return date() },
			"The {name} was released on {value}.",
			[]string{"when did the {name} come out", "{name} release date", "when was {name} released"}},
		{"sku", func(_, _ string) string {
			return fmt.Sprintf("%c%c-%04d", 'A'+rng.Intn(26), 'A'+rng.Intn(26), rng.Intn(10000))
		},
			"The SKU for the {name} is {value}.",
			[]string{"whats the sku for {name}", "{name} sku number", "product code for {name}"}},
		{"support_email", func(_, sl string) string { return "support+" + sl + "@company.example" },
			"Customer support for the {name} is available at {value}.",
			[]string{"how do i get support for {name}", "{name} support email", "where do i email about my {name}"}},
		{"warranty", func(_, _ string) string {
			return pick([]string{"1-year", "2-year", "3-year", "90-day", "limited lifetime"})
		},
			"The {name} comes with a {value} warranty.",
			[]string{"how long is the warranty on {name}", "{name} warranty", "does {name} have a warranty"}},
		{"weight", func(_, _ string) string { return fmt.Sprintf("%d grams", 80+rng.Intn(2000)) },
			"The {name} weighs {value}.",
			[]string{"how much does the {name} weigh", "{name} weight", "how heavy is {name}"}},
		{"color", func(_, _ string) string {
			return pick([]string{"black and silver", "matte white", "midnight blue", "graphite", "rose gold"})
		},
			"The {name} is available in {value}.",
			[]string{"what colors does {name} come in", "{name} color options", "what color is the {name}"}},
		{"category", func(_, _ string) string {
			return pick([]string{"audio", "networking", "accessory", "display", "computing", "smart home"})
		},
			"The {name} is categorised as a {value} product.",
			[]string{"what kind of product is the {name}", "{name} category", "what type of thing is {name}"}},
	}

	employeeAttrs := []attr{
		{"email", func(name, _ string) string {
			return strings.ReplaceAll(strings.ToLower(name), " ", ".") + "@company.example"
		},
			"{name}'s work email address is {value}.",
			[]string{"whats {name} email", "how do i email {name}", "{name}s email address", "email for {name}"}},
		{"phone", func(_, _ string) string { return fmt.Sprintf("+1-555-%04d", rng.Intn(10000)) },
			"{name}'s direct desk phone number is {value}.",
			[]string{"whats {name} phone number", "{name} desk phone", "how do i call {name}"}},
		{"title", func(_, _ string) string {
			return pick([]string{"Senior Engineer", "Product Manager", "Account Executive", "Designer", "Data Analyst", "Support Lead"})
		},
			"{name}'s official job title is {value}.",
			[]string{"whats {name} job title", "what does {name} do", "{name} role"}},
		{"department", func(_, _ string) string {
			return pick([]string{"Engineering", "Sales", "Marketing", "Finance", "People Ops", "Support"})
		},
			"{name} works in the {value} department.",
			[]string{"what team is {name} on", "{name} department", "where does {name} work"}},
		{"start_date", func(_, _ string) string { return date() },
			"{name} joined the company on {value}.",
			[]string{"when did {name} start", "{name} start date", "how long has {name} been here"}},
		{"desk_location", func(_, _ string) string { return fmt.Sprintf("%dF-%02d", 1+rng.Intn(6), 1+rng.Intn(40)) },
			"{name} sits at desk {value}.",
			[]string{"where does {name} sit", "{name} desk", "wheres {name} located"}},
	}

	officeAttrs := []attr{
		{"address", func(name, _ string) string {
			return fmt.Sprintf("%d %s Avenue, %s", 1+rng.Intn(300), pick([]string{"Maple", "Oak", "Market", "King", "Harbour"}), name)
		},
			"The {name} office is located at {value}.",
			[]string{"whats the address of the {name} office", "where is the {name} office", "{name} office address"}},
		{"phone", func(_, _ string) string { return fmt.Sprintf("+1-555-0%03d", rng.Intn(1000)) },
			"The {name} office main phone number is {value}.",
			[]string{"whats the {name} office phone", "phone number for {name} office", "how do i call the {name} office"}},
		{"opening_hours", func(_, _ string) string {
			return pick([]string{"9:00 to 17:00 on weekdays", "8:30 to 18:00 Monday to Friday", "10:00 to 16:00 weekdays"})
		},
			"The {name} office is open {value}.",
			[]string{"when is the {name} office open", "{name} office hours", "what time does the {name} office open"}},
		{"timezone", func(name, _ string) string { return tz[name] },
			"The {name} office operates in the {value} timezone.",
			[]string{"what timezone is the {name} office in", "{name} office timezone"}},
	}

	vendorAttrs := []attr{
		{"contact_email", func(_, sl string) string { return "sales@" + sl + ".example" },
			"Our primary contact at {name} can be reached at {value}.",
			[]string{"who do i contact at {name}", "{name} contact email", "email for {name}"}},
		{"account_number", func(_, _ string) string { return fmt.Sprintf("ACCT-%06d", rng.Intn(1000000)) },
			"Our account number with {name} is {value}.",
			[]string{"whats our account number with {name}", "{name} account number", "account no for {name}"}},
		{"payment_terms", func(_, _ string) string {
			return pick([]string{"net 30", "net 45", "net 60", "due on receipt", "net 15"})
		},
			"Payment terms with {name} are {value}.",
			[]string{"what are the payment terms with {name}", "{name} payment terms", "how long do we have to pay {name}"}},
		{"website", func(_, sl string) string { return "https://www." + sl + ".example" },
			"The website for {name} is {value}.",
			[]string{"whats {name} website", "{name} url", "where is {name} online"}},
	}

	// products: 80 entities x 8 attrs = 640
	for i := 0; i < 80; i++ {
		name := productAdj[i/len(productNoun)] + " " + productNoun[i%len(productNoun)]
		gen("product", name, productAttrs, &mems, &queries, 1)
	}
	// employees: 50 x 6 = 300 (unique first+last pairs)
	empCount := 0
	for li := 0; li < len(lastNames) && empCount < 50; li++ {
		for fi := 0; fi < len(firstNames) && empCount < 50; fi++ {
			name := firstNames[fi] + " " + lastNames[li]
			gen("employee", name, employeeAttrs, &mems, &queries, 1)
			empCount++
		}
	}
	// offices: 8 x 4 = 32
	for _, c := range cities {
		gen("office", c, officeAttrs, &mems, &queries, 2)
	}
	// vendors: 7 x 4 = 28
	for _, v := range vendors {
		gen("vendor", v, vendorAttrs, &mems, &queries, 2)
	}

	for i := range queries {
		queries[i].ID = fmt.Sprintf("q%04d", i+1)
	}

	dir := filepath.Join("eval", "memslot", "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	writeJSON(filepath.Join(dir, "memories.json"), mems)
	writeJSON(filepath.Join(dir, "queries.json"), queries)
	fmt.Printf("wrote %d memories and %d queries to %s\n", len(mems), len(queries), dir)
}

func writeJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		panic(err)
	}
}
