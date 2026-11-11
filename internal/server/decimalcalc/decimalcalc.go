// Package decimalcalc evaluates arithmetic expressions in exact arithmetic
// (math/big rationals), so 0.1 + 0.2 is 0.3 and 1500 * 1.08 is 1620 — no binary
// floating-point error. It is the calculator tool's engine for money and other
// exact decimals. Anything outside its small grammar returns ErrUnsupported so
// the caller can fall back to a JavaScript evaluator (Math.sqrt, methods, ...).
package decimalcalc

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// ErrUnsupported means the expression uses syntax this evaluator does not
// implement (not that it is wrong); callers may try another evaluator.
var ErrUnsupported = errors.New("unsupported syntax")

const (
	maxBits     = 200_000 // numerator+denominator size guard against runaway powers
	maxPow      = 1000
	maxRoundDig = 30
	divDigits   = 20 // decimals shown for a non-terminating result
)

// Eval evaluates expr and returns the result as text: an exact decimal when the
// value has a finite decimal expansion, otherwise rounded to 20 decimals with
// the exact fraction noted. A top-level round(x, n) prints exactly n decimals.
func Eval(expr string) (string, error) {
	toks, err := lex(expr)
	if err != nil {
		return "", err
	}
	p := &parser{toks: toks}
	n, err := p.expr()
	if err != nil {
		return "", err
	}
	if p.pos != len(p.toks) {
		if p.isOp(",") {
			// Never fall back to JS here: its comma operator would silently drop
			// everything before the comma ("1,234.56 * 2" -> 469.12).
			return "", errors.New(`unexpected ","; write numbers without thousands separators and with "." as the decimal point (1234.56)`)
		}
		return "", fmt.Errorf("%w: unexpected %q", ErrUnsupported, p.toks[p.pos].text)
	}
	return format(n), nil
}

type tokKind int

const (
	tNum tokKind = iota
	tIdent
	tOp
)

type token struct {
	kind tokKind
	text string
}

func lex(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			if j < len(s) && (s[j] == 'e' || s[j] == 'E') { // exponent: 1e3, 2.5e-2
				k := j + 1
				if k < len(s) && (s[k] == '+' || s[k] == '-') {
					k++
				}
				if k < len(s) && s[k] >= '0' && s[k] <= '9' {
					for k < len(s) && s[k] >= '0' && s[k] <= '9' {
						k++
					}
					j = k
				}
			}
			out = append(out, token{tNum, s[i:j]})
			i = j
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_':
			j := i
			for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9' || s[j] == '_') {
				j++
			}
			out = append(out, token{tIdent, s[i:j]})
			i = j
		case c == '*' && i+1 < len(s) && s[i+1] == '*':
			out = append(out, token{tOp, "^"})
			i += 2
		case strings.IndexByte("+-*/%^(),", c) >= 0:
			out = append(out, token{tOp, string(c)})
			i++
		default:
			return nil, fmt.Errorf("%w: %q", ErrUnsupported, string(c))
		}
	}
	return out, nil
}

// val is a result plus, when it came straight from round(x, n), the number of
// decimals to print (-1 otherwise).
type val struct {
	r     *big.Rat
	scale int
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() (token, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return token{}, false
}

func (p *parser) isOp(op string) bool {
	t, ok := p.peek()
	return ok && t.kind == tOp && t.text == op
}

func (p *parser) expr() (val, error) {
	l, err := p.term()
	if err != nil {
		return l, err
	}
	for p.isOp("+") || p.isOp("-") {
		op := p.toks[p.pos].text
		p.pos++
		r, err := p.term()
		if err != nil {
			return l, err
		}
		z := new(big.Rat)
		if op == "+" {
			z.Add(l.r, r.r)
		} else {
			z.Sub(l.r, r.r)
		}
		if l, err = check(z); err != nil {
			return l, err
		}
	}
	return l, nil
}

func (p *parser) term() (val, error) {
	l, err := p.unary()
	if err != nil {
		return l, err
	}
	for p.isOp("*") || p.isOp("/") || p.isOp("%") {
		op := p.toks[p.pos].text
		p.pos++
		r, err := p.unary()
		if err != nil {
			return l, err
		}
		z := new(big.Rat)
		if op == "*" {
			z.Mul(l.r, r.r)
		} else {
			if r.r.Sign() == 0 {
				return l, errors.New("division by zero")
			}
			z.Quo(l.r, r.r)
			if op == "%" { // remainder with the dividend's sign, as in JS/C: l - r*trunc(l/r)
				q := new(big.Rat).SetInt(new(big.Int).Quo(z.Num(), z.Denom()))
				z.Sub(l.r, q.Mul(q, r.r))
			}
		}
		if l, err = check(z); err != nil {
			return l, err
		}
	}
	return l, nil
}

func (p *parser) unary() (val, error) {
	if p.isOp("-") || p.isOp("+") {
		neg := p.toks[p.pos].text == "-"
		p.pos++
		v, err := p.unary()
		if err != nil {
			return v, err
		}
		if neg {
			return val{new(big.Rat).Neg(v.r), v.scale}, nil
		}
		return v, nil
	}
	return p.power()
}

func (p *parser) power() (val, error) {
	base, err := p.primary()
	if err != nil {
		return base, err
	}
	if !p.isOp("^") {
		return base, nil
	}
	p.pos++
	exp, err := p.unary() // right-associative, allows 2^-1
	if err != nil {
		return exp, err
	}
	if !exp.r.IsInt() {
		return exp, fmt.Errorf("%w: non-integer exponent", ErrUnsupported)
	}
	e := exp.r.Num()
	if !e.IsInt64() || e.Int64() > maxPow || e.Int64() < -maxPow {
		return exp, fmt.Errorf("exponent too large (limit %d)", maxPow)
	}
	n := int(e.Int64())
	neg := n < 0
	if neg {
		n = -n
	}
	if base.r.Sign() == 0 && neg {
		return base, errors.New("division by zero")
	}
	if int64(base.r.Num().BitLen()+base.r.Denom().BitLen())*int64(n) > maxBits {
		return base, errors.New("number too large")
	}
	num := new(big.Int).Exp(base.r.Num(), big.NewInt(int64(n)), nil)
	den := new(big.Int).Exp(base.r.Denom(), big.NewInt(int64(n)), nil)
	z := new(big.Rat).SetFrac(num, den)
	if neg {
		z.Inv(z)
	}
	return check(z)
}

func (p *parser) primary() (val, error) {
	t, ok := p.peek()
	if !ok {
		return val{}, errors.New("unexpected end of expression")
	}
	switch {
	case t.kind == tNum:
		p.pos++
		r, ok := new(big.Rat).SetString(t.text)
		if !ok {
			return val{}, fmt.Errorf("bad number %q", t.text)
		}
		return check(r)
	case t.kind == tOp && t.text == "(":
		p.pos++
		v, err := p.expr()
		if err != nil {
			return v, err
		}
		if !p.isOp(")") {
			return v, errors.New("missing closing parenthesis")
		}
		p.pos++
		return v, nil
	case t.kind == tIdent:
		return p.call(t.text)
	}
	return val{}, fmt.Errorf("%w: unexpected %q", ErrUnsupported, t.text)
}

var funcs = map[string]bool{"round": true, "floor": true, "ceil": true, "trunc": true, "abs": true, "min": true, "max": true}

func (p *parser) call(name string) (val, error) {
	if !funcs[name] {
		return val{}, fmt.Errorf("%w: %q", ErrUnsupported, name)
	}
	p.pos++
	if !p.isOp("(") {
		return val{}, fmt.Errorf("%w: %q is not a variable", ErrUnsupported, name)
	}
	p.pos++
	var args []val
	for !p.isOp(")") {
		if len(args) > 0 {
			if !p.isOp(",") {
				return val{}, errors.New("expected , or ) in call")
			}
			p.pos++
		}
		a, err := p.expr()
		if err != nil {
			return a, err
		}
		args = append(args, a)
	}
	p.pos++
	need := func(lo, hi int) error {
		if len(args) < lo || len(args) > hi {
			return fmt.Errorf("%s() takes %d..%d arguments, got %d", name, lo, hi, len(args))
		}
		return nil
	}
	switch name {
	case "round":
		if err := need(1, 2); err != nil {
			return val{}, err
		}
		digits := 0
		if len(args) == 2 {
			if !args[1].r.IsInt() || !args[1].r.Num().IsInt64() {
				return val{}, errors.New("round(): digits must be an integer")
			}
			digits = int(args[1].r.Num().Int64())
		}
		if digits < -maxRoundDig || digits > maxRoundDig {
			return val{}, fmt.Errorf("round(): digits must be within ±%d", maxRoundDig)
		}
		z := roundHalfUp(args[0].r, digits)
		if digits < 0 {
			digits = 0
		}
		return val{z, digits}, nil
	case "floor", "ceil", "trunc", "abs":
		if err := need(1, 1); err != nil {
			return val{}, err
		}
		x := args[0].r
		switch name {
		case "abs":
			return val{new(big.Rat).Abs(x), args[0].scale}, nil
		case "floor":
			return val{new(big.Rat).SetInt(floorDiv(x.Num(), x.Denom())), -1}, nil
		case "ceil":
			n := new(big.Int).Neg(x.Num())
			return val{new(big.Rat).SetInt(new(big.Int).Neg(floorDiv(n, x.Denom()))), -1}, nil
		default: // trunc
			return val{new(big.Rat).SetInt(new(big.Int).Quo(x.Num(), x.Denom())), -1}, nil
		}
	default: // min, max
		if len(args) < 1 {
			return val{}, fmt.Errorf("%s() needs at least one argument", name)
		}
		best := args[0].r
		for _, a := range args[1:] {
			if c := a.r.Cmp(best); name == "min" && c < 0 || name == "max" && c > 0 {
				best = a.r
			}
		}
		return val{best, -1}, nil
	}
}

func floorDiv(a, b *big.Int) *big.Int { // b > 0
	q, m := new(big.Int).DivMod(a, b, new(big.Int)) // Euclidean: m >= 0, so q is floor for b > 0
	_ = m
	return q
}

// roundHalfUp rounds to n decimals, halves away from zero (commercial rounding).
func roundHalfUp(x *big.Rat, n int) *big.Rat {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(n))), nil)
	num, den := new(big.Int).Set(x.Num()), new(big.Int).Set(x.Denom())
	if n >= 0 {
		num.Mul(num, scale)
	} else {
		den.Mul(den, scale)
	}
	neg := num.Sign() < 0
	num.Abs(num)
	// floor((2*num + den) / (2*den)) = round half up on the magnitude
	num.Mul(num, big.NewInt(2)).Add(num, den)
	q := num.Quo(num, den.Mul(den, big.NewInt(2)))
	if neg {
		q.Neg(q)
	}
	r := new(big.Rat).SetInt(q)
	if n >= 0 {
		return r.Quo(r, new(big.Rat).SetInt(scale))
	}
	return r.Mul(r, new(big.Rat).SetInt(scale))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func check(r *big.Rat) (val, error) {
	if r.Num().BitLen()+r.Denom().BitLen() > maxBits {
		return val{}, errors.New("number too large")
	}
	return val{r, -1}, nil
}

// format renders v: fixed decimals after round(), an exact decimal when the
// expansion is finite, otherwise 20 decimals plus the exact fraction.
func format(v val) string {
	if v.scale >= 0 {
		return v.r.FloatString(v.scale)
	}
	den := new(big.Int).Set(v.r.Denom())
	twos, fives := 0, 0
	two, five, zero := big.NewInt(2), big.NewInt(5), new(big.Int)
	m := new(big.Int)
	for new(big.Int).Mod(den, two).Cmp(zero) == 0 {
		den.Quo(den, two)
		twos++
	}
	for m.Mod(den, five).Cmp(zero) == 0 {
		den.Quo(den, five)
		fives++
	}
	if den.Cmp(big.NewInt(1)) == 0 {
		n := twos
		if fives > n {
			n = fives
		}
		return v.r.FloatString(n)
	}
	return fmt.Sprintf("%s (rounded to %d decimals; exact value %s)", v.r.FloatString(divDigits), divDigits, v.r.RatString())
}
