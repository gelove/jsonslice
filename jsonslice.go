package jsonslice

/**
  JsonSlice 0.3.0
  Michael Gurov, 2018
  MIT licenced

  Slice a part of a raw json ([]byte) using jsonpath, without unmarshalling the whole thing.
  The result is also []byte.
**/

import (
	"errors"
	"strconv"
)

// Get the jsonpath subset of the input
func Get(input []byte, path string) ([]byte, error) {

	if path[0] != '$' {
		return nil, errors.New("path: $ expected")
	}

	tokens, err := parsePath([]byte(path))
	if err != nil {
		return nil, err
	}

	return getValue(input, tokens)
}

const (
	// array node
	cArrayType = 1 << iota
	// array properties
	cArrBounded = 1 << iota // bounded [x:y] or indexed [x]
	// terminal node
	cIsTerminal = 1 << iota
	// function
	cFunction = 1 << iota
	// function subject
	cSubject = 1 << iota
)

type tToken struct {
	Key   string
	Type  int8 // properties
	Left  int  // >=0 index from the start, <0 backward index from the end
	Right int  // 0 till the end inclusive, >0 to index exclusive, <0 backward index from the end exclusive
	Next  *tToken
}

func parsePath(path []byte) (*tToken, error) {
	tok := &tToken{}
	i := 0
	l := len(path)
	if l == 0 {
		return nil, errors.New("path: empty")
	}
	// key
	for ; i < l && path[i] != '.' && path[i] != '[' && path[i] != '('; i++ {
	}
	tok.Key = string(path[:i])
	// type
	if i == l {
		tok.Type |= cIsTerminal
		return tok, nil
	}
	// function
	if path[i] == '(' && i < l && path[i+1] == ')' {
		switch tok.Key {
		case "length":
		case "size":
		default:
			return nil, errors.New("path: unknown function " + tok.Key + "()")
		}
		tok.Type |= cFunction
		i += 2
		if i == l {
			tok.Type |= cIsTerminal
			return tok, nil
		}
	}
	if path[i] == '[' {
		tok.Type = cArrayType
		i++
		ind := 0
		ind, i = readNumber(path, i)
		if i == l || (path[i] != ':' && path[i] != ']') {
			return nil, errors.New("path: index bound missing")
		}
		tok.Left = ind
		//
		if path[i] == ':' {
			tok.Type |= cArrBounded
			i++
			ind, ii := readNumber(path, i)
			if ind == 0 && ii > i {
				return nil, errors.New("path: 0 as a second bound does not make sense")
			}
			if ii == l || path[ii] != ']' {
				return nil, errors.New("path: index bound missing")
			}
			i = ii
			tok.Right = ind
		}
		i++
		if i == l {
			tok.Type |= cIsTerminal
			return tok, nil
		}
	}
	if tok.Type&cArrBounded > 0 && tok.Type&cIsTerminal == 0 {
		return nil, errors.New("path: indefinite references are not yet supported")
	}
	if path[i] != '.' {
		return nil, errors.New("path: invalid element reference ('.' expected)")
	}
	i++
	next, err := parsePath(path[i:])
	if err != nil {
		return nil, err
	}
	tok.Next = next
	if next.Type&cFunction > 0 {
		tok.Type |= cSubject
	}

	return tok, nil
}

func getValue(input []byte, tok *tToken) (result []byte, err error) {

	i, err := skipSpaces(input, 0)
	if err != nil {
		return nil, err
	}

	input = input[i:]
	if len(input) == 0 {
		return nil, errors.New("unexpected end of input")
	}
	if input[0] != '{' && input[0] != '[' {
		return nil, errors.New("object or array expected")
	}
	if tok.Key != "$" {
		// find the key and seek to the value
		input, err = getKeyValue(input, tok.Key)
		if err != nil {
			return nil, err
		}
	}
	// check value type
	if err = checkValueType(input, tok); err != nil {
		return nil, err
	}

	// here we are at the beginning of a value

	if tok.Type&cSubject > 0 {
		return doFunc(input, tok.Next)
	}

	if tok.Type&cIsTerminal > 0 {
		if tok.Type&cArrayType > 0 {
			return sliceArray(input, tok)
		}
		eoe, err := skipValue(input, 0)
		if err != nil {
			return nil, err
		}
		return input[:eoe], nil
	}
	if tok.Type&cArrayType > 0 {
		input, err = sliceArray(input, tok)
		if err != nil {
			return nil, err
		}
	}
	return getValue(input, tok.Next)
}

const keySeek = 1
const keyOpen = 2
const keyClose = 4

// getKeyValue: find the key and seek to the value
func getKeyValue(input []byte, key string) ([]byte, error) {
	var err error
	if input[0] != '{' {
		return nil, errors.New("object expected")
	}

	i := 1
	l := len(input)

	for i < l && input[i] != '}' {
		state := keySeek
		k := make([]byte, 0)
		for ch := input[i]; i < l && state != keyClose; ch = input[i] {
			switch state {
			case keySeek:
				if ch == '"' {
					state = keyOpen
				}
			case keyOpen:
				if ch == '"' {
					state = keyClose
				} else {
					k = append(k, byte(ch))
				}
			}
			i++
		}

		if state == keyClose {
			i, err = seekToValue(input, i)
			if err != nil {
				return nil, err
			}
			if key == string(k) {
				return input[i:], nil
			}
			i, err = skipValue(input, i)
			if err != nil {
				return nil, err
			}
		}
	}
	return nil, errors.New("field " + key + " not found")
}

type tElem struct {
	start int
	end   int
}

// sliceArray select node(s) by bound(s)
func sliceArray(input []byte, tok *tToken) ([]byte, error) {
	l := len(input)
	if input[0] != '[' {
		return nil, errors.New("array not found at " + tok.Key)
	}
	i := 1 // skip '['
	elems := make([]tElem, 0)
	// scan for elements
	for ch := input[i]; i < l && ch != ']'; ch = input[i] {
		e, err := skipValue(input, i)
		if err != nil {
			return nil, err
		}
		elems = append(elems, tElem{i, e})
		// skip spaces after value
		i, err = skipSpaces(input, e)
		if err != nil {
			return nil, err
		}
	}
	//   select by index(es)
	if tok.Type&cArrBounded == 0 {
		a := tok.Left
		if a < 0 {
			a += len(elems)
		}
		if a < 0 || a >= len(elems) {
			return nil, errors.New(tok.Key + "[" + strconv.Itoa(tok.Left) + "] does not exist")
		}
		return input[elems[a].start:elems[a].end], nil
	}
	// two bounds
	a := tok.Left
	b := tok.Right
	if b == 0 {
		b = len(elems)
	}
	if a < 0 {
		a += len(elems)
	}
	if b < 0 {
		b += len(elems)
	}
	b-- // right bound excluded
	if a < 0 || a >= len(elems) || b < 0 || b >= len(elems) {
		return nil, errors.New(tok.Key + "[" + strconv.Itoa(tok.Left) + ":" + strconv.Itoa(tok.Right) + "] does not exist")
	}
	return append([]byte{'['}, append(input[elems[a].start:elems[b].end], ']')...), nil
}

// sliceValue: slice a single value
func sliceValue(input []byte) ([]byte, error) {
	eoe, err := skipValue(input, 0)
	if err != nil {
		return nil, err
	}
	return input[:eoe], nil
}

// getValues: get (sub-)values from array
func getValues(input []byte, tok *tToken) ([]byte, error) {
	return nil, errors.New("not yet supported")
}

func seekToValue(input []byte, i int) (int, error) {
	var err error
	// spaces before ':'
	i, err = skipSpaces(input, i)
	if err != nil {
		return 0, err
	}
	if input[i] != ':' {
		return 0, errors.New("\":\" expected")
	}
	i++ // colon
	return skipSpaces(input, i)
}

func skipValue(input []byte, i int) (int, error) {
	var err error
	// spaces
	i, err = skipSpaces(input, i)
	if err != nil {
		return 0, err
	}

	l := len(input)
	if input[i] == '"' {
		// string
		return skipString(input, i)
	} else if input[i] == '{' || input[i] == '[' {
		// object or array
		mark := input[i]
		unmark := mark + 2
		nested := 0
		instr := false
		prev := mark
		i++
		for ch := input[i]; i < l && !(ch == unmark && nested == 0); ch = input[i] {
			if ch == '"' {
				if prev != '\\' {
					instr = !instr
				}
			} else if !instr {
				if ch == mark {
					nested++
				} else if ch == unmark {
					nested--
				}
			}
			prev = ch
			i++
		}
		if i == l {
			return 0, errors.New("unexpected end of input")
		}
		i++ // closing mark
	} else {
		// number, bool, null
		for ch := input[i]; i < l && ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' && ch != ',' && ch != '}'; ch = input[i] {
			i++
		}
	}
	return i, nil
}

func checkValueType(input []byte, tok *tToken) error {
	if len(input) < 2 {
		return errors.New("unexpected end of input")
	}
	if tok.Type&cSubject > 0 {
		return nil
	}
	ch := input[0]
	if ch == '[' && tok.Type&cArrayType == 0 && tok.Type&cIsTerminal == 0 {
		return errors.New("object expected at " + tok.Key)
	} else if ch == '{' && tok.Type&cArrayType > 0 {
		return errors.New("array expected at " + tok.Key)
	} else if ch != '{' && ch != '[' && tok.Type&cIsTerminal == 0 {
		return errors.New("complex type expected at " + tok.Key)
	}
	return nil
}

// get input and current value position
// return next key and new value position

func nextKey(input []byte, i int) ([]byte, int) {
	status := keySeek
	key := make([]byte, 0)
	for l := len(input); i < l; i++ {
		ch := input[i]
		switch {
		case status&keyOpen > 0:
			if ch == '"' {
				status -= keyOpen
				status |= keyClose
			} else {
				key = append(key, ch)
			}
		case status&keySeek > 0 && ch == '"':
			status -= keySeek
			status |= keyOpen
		case status&keyClose > 0 && ch == ':':
			return key, i + 1
		}
	}
	return nil, i
}

func doFunc(input []byte, tok *tToken) ([]byte, error) {
	var err error
	var result int
	switch tok.Key {
	case "size":
		result, err = skipValue(input, 0)
	case "length":
		if input[0] == '"' {
			result, err = skipString(input, 0)
		} else if input[0] == '[' {
			i := 1
			l := len(input)
			// count elements
			for ch := input[i]; i < l && ch != ']'; ch = input[i] {
				e, err := skipValue(input, i)
				if err != nil {
					return nil, err
				}
				result++
				// skip spaces after value
				i, err = skipSpaces(input, e)
				if err != nil {
					return nil, err
				}
			}
		} else {
			return nil, errors.New("length() is only applicable to array or string")
		}
	}
	if err != nil {
		return nil, err
	}
	return []byte(strconv.Itoa(result)), nil
}

// readNumber returns the array index specified in array bound clause
func readNumber(path []byte, i int) (int, int) {
	sign := 1
	l := len(path)
	ind := 0
	for ch := path[i]; i < l && (ch == '-' || (ch >= '0' && ch <= '9')); ch = path[i] {
		if ch == '-' {
			sign = -1
		} else {
			ind = ind*10 + int(ch-'0')
		}
		i++
	}
	return ind * sign, i
}

func skipSpaces(input []byte, i int) (int, error) {
	l := len(input)
	for ch := input[i]; i < l && (ch == ',' || ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'); ch = input[i] {
		i++
	}
	if i == l {
		return 0, errors.New("unexpected end of input")
	}
	return i, nil
}

func skipString(input []byte, i int) (int, error) {
	prev := byte('"')
	done := false
	i++
	l := len(input)
	for ch := input[i]; i < l && !done; ch = input[i] {
		if ch == '"' && prev != '\\' {
			done = true
		}
		prev = ch
		i++
	}
	if i == l {
		return 0, errors.New("unexpected end of input")
	}
	return i, nil
}
