package connect

type Client struct{}

func NewClient(baseURL string) *Client { return &Client{} }
