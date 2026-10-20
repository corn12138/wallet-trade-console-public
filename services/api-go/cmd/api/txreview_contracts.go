package main

func (k knownSpendersLookup) ContractAddress(chainID int, name string) (string, bool) {
	if k.loader == nil {
		return "", false
	}
	registry, err := k.loader.Load()
	if err != nil {
		return "", false
	}
	contract, ok := registry[chainID].Contracts[name]
	return contract.Address, ok && contract.Address != ""
}
