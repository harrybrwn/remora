package rabbitmq

import "fmt"

type Error struct {
	Err    string `json:"error"`
	Reason string `json:"reason"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("rabbitmq: %s %s", e.Err, e.Reason)
}

type Overview struct {
	ManagementVersion       string `json:"management_version"`
	RatesMode               string `json:"rates_mode"`
	SampleRetentionPolicies struct {
		Global   []int `json:"global"`
		Basic    []int `json:"basic"`
		Detailed []int `json:"detailed"`
	} `json:"sample_retention_policies"`
	ExchangeTypes []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Enabled     bool   `json:"enabled"`
	} `json:"exchange_types"`
	ProductVersion    string       `json:"product_version"`
	ProductName       string       `json:"product_name"`
	RabbitmqVersion   string       `json:"rabbitmq_version"`
	ClusterName       string       `json:"cluster_name"`
	ErlangVersion     string       `json:"erlang_version"`
	ErlangFullVersion string       `json:"erlang_full_version"`
	DisableStats      bool         `json:"disable_stats"`
	EnableQueueTotals bool         `json:"enable_queue_totals"`
	MessageStats      MessageStats `json:"message_stats"`
	ChurnRates        struct {
		ChannelClosed         int     `json:"channel_closed"`
		ChannelClosedDetails  details `json:"channel_closed_details"`
		ChannelCreated        int     `json:"channel_created"`
		ChannelCreatedDetails struct {
			Rate float64 `json:"rate"`
		} `json:"channel_created_details"`
		ConnectionClosed        int `json:"connection_closed"`
		ConnectionClosedDetails struct {
			Rate float64 `json:"rate"`
		} `json:"connection_closed_details"`
		ConnectionCreated        int `json:"connection_created"`
		ConnectionCreatedDetails struct {
			Rate float64 `json:"rate"`
		} `json:"connection_created_details"`
		QueueCreated        int `json:"queue_created"`
		QueueCreatedDetails struct {
			Rate float64 `json:"rate"`
		} `json:"queue_created_details"`
		QueueDeclared        int     `json:"queue_declared"`
		QueueDeclaredDetails details `json:"queue_declared_details"`
		QueueDeleted         int     `json:"queue_deleted"`
		QueueDeletedDetails  details `json:"queue_deleted_details"`
	} `json:"churn_rates"`
	QueueTotals struct {
		Messages                      int     `json:"messages"`
		MessagesDetails               details `json:"messages_details"`
		MessagesReady                 int     `json:"messages_ready"`
		MessagesReadyDetails          details `json:"messages_ready_details"`
		MessagesUnacknowledged        int     `json:"messages_unacknowledged"`
		MessagesUnacknowledgedDetails details `json:"messages_unacknowledged_details"`
	} `json:"queue_totals"`
	ObjectTotals struct {
		Channels    int `json:"channels"`
		Connections int `json:"connections"`
		Consumers   int `json:"consumers"`
		Exchanges   int `json:"exchanges"`
		Queues      int `json:"queues"`
	} `json:"object_totals"`
	StatisticsDbEventQueue int    `json:"statistics_db_event_queue"`
	Node                   string `json:"node"`
	Listeners              []struct {
		Node      string `json:"node"`
		Protocol  string `json:"protocol"`
		IPAddress string `json:"ip_address"`
		Port      int    `json:"port"`
		// SocketOpts struct {
		// 	Backlog     int           `json:"backlog"`
		// 	Nodelay     bool          `json:"nodelay"`
		// 	Linger      []interface{} `json:"linger"`
		// 	ExitOnClose bool          `json:"exit_on_close"`
		// 	CowboyOpts  struct {
		// 		Sendfile bool `json:"sendfile"`
		// 	} `json:"cowboy_opts"`
		// 	Port     int    `json:"port"`
		// 	Protocol string `json:"protocol"`
		// } `json:"socket_opts,omitempty"`
	} `json:"listeners"`
	Contexts []struct {
		SslOpts     []interface{} `json:"ssl_opts"`
		Node        string        `json:"node"`
		Description string        `json:"description"`
		Path        string        `json:"path"`
		CowboyOpts  string        `json:"cowboy_opts"`
		Port        string        `json:"port"`
		Protocol    string        `json:"protocol,omitempty"`
	} `json:"contexts"`
}

type Channel struct {
	Name              string `json:"name"`
	Number            int    `json:"number"`
	Node              string `json:"node"`
	AcksUncommitted   int    `json:"acks_uncommitted"`
	Confirm           bool   `json:"confirm"`
	ConnectionDetails struct {
		Name     string `json:"name"`
		PeerHost string `json:"peer_host"`
		PeerPort int    `json:"peer_port"`
	} `json:"connection_details"`
	ConsumerCount     int `json:"consumer_count"`
	GarbageCollection struct {
		FullsweepAfter  int `json:"fullsweep_after"`
		MaxHeapSize     int `json:"max_heap_size"`
		MinBinVheapSize int `json:"min_bin_vheap_size"`
		MinHeapSize     int `json:"min_heap_size"`
		MinorGcs        int `json:"minor_gcs"`
	} `json:"garbage_collection"`
	GlobalPrefetchCount    int          `json:"global_prefetch_count"`
	IdleSince              string       `json:"idle_since,omitempty"`
	MessagesUnacknowledged int          `json:"messages_unacknowledged"`
	MessagesUncommitted    int          `json:"messages_uncommitted"`
	MessagesUnconfirmed    int          `json:"messages_unconfirmed"`
	PendingRaftCommands    int          `json:"pending_raft_commands"`
	PrefetchCount          int          `json:"prefetch_count"`
	Reductions             int          `json:"reductions"`
	ReductionsDetails      details      `json:"reductions_details"`
	State                  string       `json:"state"`
	Transactional          bool         `json:"transactional"`
	User                   string       `json:"user"`
	UserWhoPerformedAction string       `json:"user_who_performed_action"`
	Vhost                  string       `json:"vhost"`
	MessageStats           MessageStats `json:"message_stats,omitempty"`
}

type Queue struct {
	Arguments struct {
	} `json:"arguments"`
	AutoDelete         bool `json:"auto_delete"`
	BackingQueueStatus struct {
		AvgAckEgressRate  float64       `json:"avg_ack_egress_rate"`
		AvgAckIngressRate float64       `json:"avg_ack_ingress_rate"`
		AvgEgressRate     float64       `json:"avg_egress_rate"`
		AvgIngressRate    float64       `json:"avg_ingress_rate"`
		Delta             []interface{} `json:"delta"`
		Len               int           `json:"len"`
		Mode              string        `json:"mode"`
		NextSeqID         int           `json:"next_seq_id"`
		Q1                int           `json:"q1"`
		Q2                int           `json:"q2"`
		Q3                int           `json:"q3"`
		Q4                int           `json:"q4"`
		// TargetRAMCount    string        `json:"target_ram_count"`
		TargetRAMCount interface{} `json:"target_ram_count"`
	} `json:"backing_queue_status"`
	ConsumerCapacity          float64     `json:"consumer_capacity"`
	ConsumerUtilisation       float64     `json:"consumer_utilisation"`
	Consumers                 int         `json:"consumers"`
	Durable                   bool        `json:"durable"`
	Exclusive                 bool        `json:"exclusive"`
	ExclusiveConsumerTag      interface{} `json:"exclusive_consumer_tag"`
	EffectivePolicyDefinition struct {
	} `json:"effective_policy_definition"`
	GarbageCollection struct {
		FullsweepAfter  int `json:"fullsweep_after"`
		MaxHeapSize     int `json:"max_heap_size"`
		MinBinVheapSize int `json:"min_bin_vheap_size"`
		MinHeapSize     int `json:"min_heap_size"`
		MinorGcs        int `json:"minor_gcs"`
	} `json:"garbage_collection"`
	HeadMessageTimestamp          interface{}  `json:"head_message_timestamp"`
	IdleSince                     string       `json:"idle_since,omitempty"`
	Memory                        int          `json:"memory"`
	MessageBytes                  int          `json:"message_bytes"`
	MessageBytesPagedOut          int          `json:"message_bytes_paged_out"`
	MessageBytesPersistent        int          `json:"message_bytes_persistent"`
	MessageBytesRAM               int          `json:"message_bytes_ram"`
	MessageBytesReady             int          `json:"message_bytes_ready"`
	MessageBytesUnacknowledged    int          `json:"message_bytes_unacknowledged"`
	MessageStats                  MessageStats `json:"message_stats,omitempty"`
	Messages                      int          `json:"messages"`
	MessagesDetails               details      `json:"messages_details"`
	MessagesPagedOut              int          `json:"messages_paged_out"`
	MessagesPersistent            int          `json:"messages_persistent"`
	MessagesRAM                   int          `json:"messages_ram"`
	MessagesReady                 int          `json:"messages_ready"`
	MessagesReadyDetails          details      `json:"messages_ready_details"`
	MessagesReadyRAM              int          `json:"messages_ready_ram"`
	MessagesUnacknowledged        int          `json:"messages_unacknowledged"`
	MessagesUnacknowledgedDetails details      `json:"messages_unacknowledged_details"`
	MessagesUnacknowledgedRAM     int          `json:"messages_unacknowledged_ram"`
	Name                          string       `json:"name"`
	Node                          string       `json:"node"`
	OperatorPolicy                interface{}  `json:"operator_policy"`
	Policy                        interface{}  `json:"policy"`
	RecoverableSlaves             interface{}  `json:"recoverable_slaves"`
	Reductions                    int          `json:"reductions"`
	ReductionsDetails             details      `json:"reductions_details"`
	SingleActiveConsumerTag       interface{}  `json:"single_active_consumer_tag"`
	State                         string       `json:"state"`
	Type                          string       `json:"type"`
	Vhost                         string       `json:"vhost"`
}

type Connection struct {
	AuthMechanism    string `json:"auth_mechanism"`
	ChannelMax       int    `json:"channel_max"`
	Channels         int    `json:"channels"`
	ClientProperties struct {
		Capabilities struct {
			ConnectionBlocked    bool `json:"connection.blocked"`
			ConsumerCancelNotify bool `json:"consumer_cancel_notify"`
		} `json:"capabilities"`
		Product        string `json:"product"`
		Version        string `json:"version"`
		ConnectionName string `json:"connection_name"`
	} `json:"client_properties"`
	ConnectedAt       int64 `json:"connected_at"`
	FrameMax          int   `json:"frame_max"`
	GarbageCollection struct {
		FullsweepAfter  int `json:"fullsweep_after"`
		MaxHeapSize     int `json:"max_heap_size"`
		MinBinVheapSize int `json:"min_bin_vheap_size"`
		MinHeapSize     int `json:"min_heap_size"`
		MinorGcs        int `json:"minor_gcs"`
	} `json:"garbage_collection"`
	Host                   string      `json:"host"`
	Name                   string      `json:"name"`
	Node                   string      `json:"node"`
	PeerCertIssuer         interface{} `json:"peer_cert_issuer"`
	PeerCertSubject        interface{} `json:"peer_cert_subject"`
	PeerCertValidity       interface{} `json:"peer_cert_validity"`
	PeerHost               string      `json:"peer_host"`
	PeerPort               int         `json:"peer_port"`
	Port                   int         `json:"port"`
	Protocol               string      `json:"protocol"`
	RecvCnt                int         `json:"recv_cnt"`
	RecvOct                int         `json:"recv_oct"`
	RecvOctDetails         details     `json:"recv_oct_details"`
	Reductions             int         `json:"reductions"`
	ReductionsDetails      details     `json:"reductions_details"`
	SendCnt                int         `json:"send_cnt"`
	SendOct                int         `json:"send_oct"`
	SendOctDetails         details     `json:"send_oct_details"`
	SendPend               int         `json:"send_pend"`
	Ssl                    bool        `json:"ssl"`
	SslCipher              interface{} `json:"ssl_cipher"`
	SslHash                interface{} `json:"ssl_hash"`
	SslKeyExchange         interface{} `json:"ssl_key_exchange"`
	SslProtocol            interface{} `json:"ssl_protocol"`
	State                  string      `json:"state"`
	Timeout                int         `json:"timeout"`
	Type                   string      `json:"type"`
	User                   string      `json:"user"`
	UserProvidedName       string      `json:"user_provided_name"`
	UserWhoPerformedAction string      `json:"user_who_performed_action"`
	Vhost                  string      `json:"vhost"`
}

type MessageStats struct {
	Confirm                 int     `json:"confirm"`
	ConfirmDetails          details `json:"confirm_details"`
	DropUnroutable          int     `json:"drop_unroutable"`
	DropUnroutableDetails   details `json:"drop_unroutable_details"`
	Publish                 int     `json:"publish"`
	PublishDetails          details `json:"publish_details"`
	ReturnUnroutable        int     `json:"return_unroutable"`
	ReturnUnroutableDetails details `json:"return_unroutable_details"`
	Ack                     int     `json:"ack"`
	AckDetails              details `json:"ack_details"`
	Deliver                 int     `json:"deliver"`
	DeliverDetails          details `json:"deliver_details"`
	DeliverGet              int     `json:"deliver_get"`
	DeliverGetDetails       details `json:"deliver_get_details"`
	DeliverNoAck            int     `json:"deliver_no_ack"`
	DeliverNoAckDetails     details `json:"deliver_no_ack_details"`
	Get                     int     `json:"get"`
	GetDetails              details `json:"get_details"`
	GetEmpty                int     `json:"get_empty"`
	GetEmptyDetails         details `json:"get_empty_details"`
	GetNoAck                int     `json:"get_no_ack"`
	GetNoAckDetails         details `json:"get_no_ack_details"`
	Redeliver               int     `json:"redeliver"`
	RedeliverDetails        details `json:"redeliver_details"`
}

type details struct {
	Avg     float64 `json:"avg"`
	AvgRate float64 `json:"avg_rate"`
	Rate    float64 `json:"rate"`
	Samples []struct {
		Sample    int   `json:"sample"`
		Timestamp int64 `json:"timestamp"`
	} `json:"samples"`
}
