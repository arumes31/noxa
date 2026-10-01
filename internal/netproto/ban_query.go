package netproto

// BanQuery reads descending ban IDs. Zero BeforeID starts at the newest ban;
// zero Limit selects the default page size. Each page checks current access.
type BanQuery struct {
	BeforeID int64 `json:"before_id,omitempty"`
	Limit    int   `json:"limit,omitempty"`
}

// BanPage reuses native ban entries, including Unix-second timestamps.
// NextBeforeID is zero at the end; otherwise use it as the next BeforeID.
type BanPage struct {
	Bans         []BanEntry `json:"bans"`
	NextBeforeID int64      `json:"next_before_id,omitempty"`
}
