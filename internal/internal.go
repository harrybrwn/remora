package internal

//go:generate mockgen -source=../event/event.go -destination=./mock/mockevent/event.go -package=mockevent
//go:generate mockgen -source=./que/amqp.go -destination=./mock/mockque/amqp.go -package=mockque
