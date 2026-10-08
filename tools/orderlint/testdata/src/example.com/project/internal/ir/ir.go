package ir

func ranges(values map[string]int, list []int) {
	for range values { // want "range over map is nondeterministic"
	}
	//trasloco:unordered-ok only membership is observed
	for range values {
	}
	for range values { //trasloco:unordered-ok values are accumulated commutatively
	}
	//trasloco:unordered-ok
	for range values { // want "range over map is nondeterministic"
	}
	for range list {
	}
}
