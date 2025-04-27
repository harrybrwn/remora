package internal

//go:generate go tool mockgen -source=../event/event.go -destination=./mock/mockevent/event.go -package=mockevent
//go:generate go tool mockgen -source=./que/amqp.go -destination=./mock/mockque/amqp.go -package=mockque
