package llm

import "net/http"

type requestAlias = http.Request
type responseAlias = http.Response

func httpClient(config Config) HTTPDoer {
	if config.HTTP != nil {
		return config.HTTP
	}
	return http.DefaultClient
}
