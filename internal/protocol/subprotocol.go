package protocol

import "strings"

const (
	SubprotocolLegacy         = "tunnelmesh.v1"
	SubprotocolOpenResult     = "tunnelmesh.v1.open-result"
	SubprotocolFlowControl    = "tunnelmesh.v1.open-result.flow-control"
	SubprotocolClientMetadata = "tunnelmesh.v1.open-result.flow-control.metadata"
)

func ClientSubprotocols() []string {
	return []string{SubprotocolClientMetadata, SubprotocolFlowControl, SubprotocolOpenResult, SubprotocolLegacy}
}

func SelectSubprotocol(offered []string) (string, bool) {
	normalized := make([]string, 0, len(offered))
	for _, header := range offered {
		for _, value := range strings.Split(header, ",") {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				normalized = append(normalized, trimmed)
			}
		}
	}
	offered = normalized
	for _, candidate := range []string{SubprotocolClientMetadata, SubprotocolFlowControl, SubprotocolOpenResult} {
		for _, value := range offered {
			if value == candidate {
				return candidate, true
			}
		}
	}
	for _, value := range offered {
		if value == SubprotocolLegacy {
			return SubprotocolLegacy, false
		}
	}
	return "", false
}
