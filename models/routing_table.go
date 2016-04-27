package models

import (
	"fmt"
	"reflect"

	routing_api_models "github.com/cloudfoundry-incubator/routing-api/models"
)

type RoutingKey struct {
	Port uint16
}

type BackendServerInfo struct {
	Address         string
	Port            uint16
	ModificationTag routing_api_models.ModificationTag
}

type BackendServerKey struct {
	Address string
	Port    uint16
}

type BackendServerDetails struct {
	ModificationTag routing_api_models.ModificationTag
}

type RoutingTableEntry struct {
	Backends map[BackendServerKey]BackendServerDetails
}

type RoutingTable struct {
	Entries map[RoutingKey]RoutingTableEntry
}

func NewRoutingTableEntry(backends []BackendServerInfo) RoutingTableEntry {
	routingTableEntry := RoutingTableEntry{
		Backends: make(map[BackendServerKey]BackendServerDetails),
	}
	for _, backend := range backends {
		backendServerKey := BackendServerKey{Address: backend.Address, Port: backend.Port}
		backendServerDetails := BackendServerDetails{ModificationTag: backend.ModificationTag}
		routingTableEntry.Backends[backendServerKey] = backendServerDetails
	}
	return routingTableEntry
}

func NewRoutingTable() RoutingTable {
	return RoutingTable{
		Entries: make(map[RoutingKey]RoutingTableEntry),
	}
}

func (table RoutingTable) serverKeyDetailsFromInfo(info BackendServerInfo) (BackendServerKey, BackendServerDetails) {
	return BackendServerKey{Address: info.Address, Port: info.Port}, BackendServerDetails{ModificationTag: info.ModificationTag}
}

func (table RoutingTable) Set(key RoutingKey, newEntry RoutingTableEntry) bool {
	existingEntry, ok := table.Entries[key]
	if ok == true && reflect.DeepEqual(existingEntry, newEntry) {
		return false
	}
	table.Entries[key] = newEntry
	return true
}

func (table RoutingTable) UpsertBackendServerKey(key RoutingKey, info BackendServerInfo) bool {
	existingEntry, routingKeyFound := table.Entries[key]
	if !routingKeyFound {
		existingEntry = NewRoutingTableEntry([]BackendServerInfo{info})
		table.Entries[key] = existingEntry
		return true
	}

	backendServerKey, backendServerDetails := table.serverKeyDetailsFromInfo(info)
	existingBackendServerDetails, backendFound := existingEntry.Backends[backendServerKey]
	if !backendFound ||
		existingBackendServerDetails.ModificationTag.SucceededBy(&backendServerDetails.ModificationTag) {
		existingEntry.Backends[backendServerKey] = backendServerDetails
		return true
	}

	return false
}

func (table RoutingTable) DeleteBackendServerKey(key RoutingKey, info BackendServerInfo) bool {
	backendServerKey, newDetails := table.serverKeyDetailsFromInfo(info)
	existingEntry, routingKeyFound := table.Entries[key]

	if routingKeyFound {
		existingDetails, backendFound := existingEntry.Backends[backendServerKey]
		if backendFound && existingDetails.ModificationTag.IsCurrentOrOlder(&newDetails.ModificationTag) {
			delete(existingEntry.Backends, backendServerKey)
			if len(existingEntry.Backends) == 0 {
				delete(table.Entries, key)
			}
			return true
		}
	}
	return false
}

func (table RoutingTable) Get(key RoutingKey) RoutingTableEntry {
	return table.Entries[key]
}

func (table RoutingTable) Size() int {
	return len(table.Entries)
}

func (k RoutingKey) String() string {
	return fmt.Sprintf("%d", k.Port)
}
