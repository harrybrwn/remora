package internal

func UnwrapAll(err error) error {
	var e error = err
	type unwrappable interface {
		Unwrap() error
	}
	unwrapper, ok := e.(unwrappable)
	for ok {
		e = unwrapper.Unwrap()
		unwrapper, ok = e.(unwrappable)
	}
	return e
}
