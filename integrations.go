package main

import (
	globalLogger "github.com/rs/zerolog/log"
)

type AgentIntegration interface {
	IsEnabled() bool
	Apply()
}

func runIntegrations() {
	globalLogger.Info().Msg("Running integrations...")

	allIntegrations := []AgentIntegration{&IntegrationGKE{}}

	for _, integration := range allIntegrations {
		if integration.IsEnabled() {
			integration.Apply()
		}
	}
}
