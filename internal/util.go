package internal

import "net"

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

func IPAddrs(host string) (v4, v6 interface{}, err error) {
	var addrs []net.IP
	addrs, err = net.LookupIP(host)
	if err != nil {
		return
	}
	for _, addr := range addrs {
		if addr.To4() != nil {
			v4 = addr.String()
		} else if addr.To16() != nil {
			v6 = addr.String()
		}
	}
	if v4 == "" {
		v4 = nil
	}
	if v6 == "" {
		v6 = nil
	}
	return
}
