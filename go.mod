module github.com/EduGoGroup/wapp-edge-intent

go 1.26.0

require github.com/EduGoGroup/wapp-shared/intents v0.1.0

// Contrato de intenciones consumido por réplica local hasta cortar intents/v0.1.0.
replace github.com/EduGoGroup/wapp-shared/intents => ../../shared/wapp-shared/intents // TODO(029): retirar al cortar intents/v0.1.0 (realineo)
