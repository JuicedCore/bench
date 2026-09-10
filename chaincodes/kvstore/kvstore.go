// kvstore is the chaincode used by the Fabric and Drunix adapters for the
// normalized workloads:
//
//	Put(key, value)               -> TxWrite
//	Get(key)                      -> TxRead
//	Transfer(from, to, amount)    -> TxTransfer (read-modify-write two accounts)
//
// It is deliberately minimal: no access control, no events, no rich queries, so
// the measurement reflects the platform's execute/order/validate/commit path and
// not chaincode logic. Account balances for Transfer are created lazily with a
// default balance so the load generator does not need a seeding phase.
package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// defaultBalance is the starting balance for an account referenced by Transfer
// before it has been seen. Large enough that a long run of unit transfers from
// any single account does not underflow.
const defaultBalance int64 = 1_000_000_000

// SmartContract implements the kvstore contract.
type SmartContract struct {
	contractapi.Contract
}

// Put sets key to value.
func (s *SmartContract) Put(ctx contractapi.TransactionContextInterface, key string, value string) error {
	if key == "" {
		return fmt.Errorf("key must not be empty")
	}
	return ctx.GetStub().PutState(key, []byte(value))
}

// Get returns the value at key, or an empty string if absent.
func (s *SmartContract) Get(ctx contractapi.TransactionContextInterface, key string) (string, error) {
	b, err := ctx.GetStub().GetState(key)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", key, err)
	}
	return string(b), nil
}

// Exists reports whether key is set.
func (s *SmartContract) Exists(ctx contractapi.TransactionContextInterface, key string) (bool, error) {
	b, err := ctx.GetStub().GetState(key)
	if err != nil {
		return false, err
	}
	return b != nil, nil
}

// Transfer moves amount from one account to another, reading and writing both
// balances so the transaction produces a realistic read/write set and is subject
// to MVCC conflict on contended accounts.
func (s *SmartContract) Transfer(ctx contractapi.TransactionContextInterface, from string, to string, amountStr string) error {
	amount, err := strconv.ParseInt(amountStr, 10, 64)
	if err != nil || amount < 0 {
		return fmt.Errorf("invalid amount %q", amountStr)
	}
	if from == to {
		return fmt.Errorf("from and to must differ")
	}
	fromBal, err := s.balance(ctx, from)
	if err != nil {
		return err
	}
	toBal, err := s.balance(ctx, to)
	if err != nil {
		return err
	}
	if fromBal < amount {
		return fmt.Errorf("insufficient balance in %s: have %d need %d", from, fromBal, amount)
	}
	if err := s.setBalance(ctx, from, fromBal-amount); err != nil {
		return err
	}
	return s.setBalance(ctx, to, toBal+amount)
}

// Balance returns an account balance (creating the lazy default in-memory only).
func (s *SmartContract) Balance(ctx contractapi.TransactionContextInterface, account string) (int64, error) {
	return s.balance(ctx, account)
}

type acct struct {
	Balance int64 `json:"balance"`
}

func (s *SmartContract) balance(ctx contractapi.TransactionContextInterface, account string) (int64, error) {
	b, err := ctx.GetStub().GetState(acctKey(account))
	if err != nil {
		return 0, fmt.Errorf("read balance %s: %w", account, err)
	}
	if b == nil {
		return defaultBalance, nil
	}
	var a acct
	if err := json.Unmarshal(b, &a); err != nil {
		return 0, fmt.Errorf("decode balance %s: %w", account, err)
	}
	return a.Balance, nil
}

func (s *SmartContract) setBalance(ctx contractapi.TransactionContextInterface, account string, bal int64) error {
	b, err := json.Marshal(acct{Balance: bal})
	if err != nil {
		return err
	}
	return ctx.GetStub().PutState(acctKey(account), b)
}

func acctKey(account string) string { return "acct/" + account }

func main() {
	cc, err := contractapi.NewChaincode(&SmartContract{})
	if err != nil {
		panic(fmt.Sprintf("create kvstore chaincode: %v", err))
	}
	if err := cc.Start(); err != nil {
		panic(fmt.Sprintf("start kvstore chaincode: %v", err))
	}
}
