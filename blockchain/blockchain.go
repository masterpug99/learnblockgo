package blockchain

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/dgraph-io/badger"
)

const (
	dbPath      = "./tmp/blocks"
	dbFile      = "./tmp/blocks/MANIFEST"
	genesisData = "First Transaction from Genesis"
)

type BlockChain struct {
	LastHash []byte
	Database *badger.DB
}

type BlockChainIterator struct {
	CurrentHash []byte
	Database    *badger.DB
}

// 블록체인이 이미 DB에 있는지 확인
func DBexists() bool {
	if _, err := os.Stat(dbFile); os.IsNotExist(err) {
		return false
	}
	return true
}

// 블록체인 초기화 함수
func InitBlockChain(address string) *BlockChain {
	var lastHash []byte

	if DBexists() {
		fmt.Println("Blockchain already Exists")
		runtime.Goexit()
	}

	opts := badger.DefaultOptions(dbPath)
	opts.Dir = dbPath
	opts.ValueDir = dbPath

	db, err := badger.Open(opts)
	Handle(err)

	err = db.Update(func(txn *badger.Txn) error {
		cbtx := CoinbaseTx(address, genesisData)
		genesis := Genesis(cbtx)
		fmt.Println("Genesis Created")
		err = txn.Set(genesis.Hash, genesis.Serialize())
		Handle(err)
		err = txn.Set([]byte("lh"), genesis.Hash)

		lastHash = genesis.Hash

		return err
	})
	Handle(err)

	blockchain := BlockChain{lastHash, db}
	return &blockchain
}

// 블록체인에서 마지막 블록의 해쉬값을 가져옴?
func ContinueBlockChain(address string) *BlockChain {
	if !DBexists() {
		fmt.Println("No existing Blockchain found, Create One")
	}

	var lastHash []byte

	opts := badger.DefaultOptions(dbPath)
	opts.Dir = dbPath
	opts.ValueDir = dbPath

	db, err := badger.Open(opts)
	Handle(err)

	err = db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("lh"))
		Handle(err)
		err = item.Value(func(val []byte) error {
			lastHash = append(lastHash, val...)
			return nil
		})
		return err
	})
	Handle(err)

	chain := BlockChain{lastHash, db}
	return &chain
}

// 블록체인 마지막 부분에 블럭을 추가
func (chain *BlockChain) AddBlock(transaction []*Transaction) {
	var lastHash []byte

	err := chain.Database.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("lh"))
		Handle(err)
		err = item.Value(func(val []byte) error {
			lastHash = append(lastHash, val...)
			return nil
		})
		return err
	})
	Handle(err)

	newBlock := CreateBlock(transaction, lastHash)

	err = chain.Database.Update(func(txn *badger.Txn) error {
		err := txn.Set(newBlock.Hash, newBlock.Serialize())
		Handle(err)
		err = txn.Set([]byte("lh"), newBlock.Hash)

		chain.LastHash = newBlock.Hash
		return err
	})
	Handle(err)
}

func (chain *BlockChain) Iterator() *BlockChainIterator {
	iter := &BlockChainIterator{chain.LastHash, chain.Database}
	return iter
}

// 체인을 순회하면서 블록을 반환
func (iter *BlockChainIterator) Next() *Block {
	var block *Block
	err := iter.Database.View(func(txn *badger.Txn) error {
		item, err := txn.Get(iter.CurrentHash)
		Handle(err)
		err = item.Value(func(val []byte) error {
			block = Deserialize(val)
			return nil
		})
		return err
	})
	Handle(err)

	iter.CurrentHash = block.PrevHash
	return block
}

// 마사용 출력을 포함하는 거래를 반환하는 함수
func (chain *BlockChain) FindUnspentTransation(pubKeyHash []byte) []Transaction {
	var unspentTxs []Transaction

	spentTXOs := make(map[string][]int)

	iter := chain.Iterator()

	for {
		block := iter.Next()

		for _, tx := range block.Transactions {
			txID := hex.EncodeToString(tx.ID)

		Outputs:
			for outIdx, out := range tx.Outputs {
				if spentTXOs[txID] != nil { // 사용한 출력이 있을 경우
					for _, spentOut := range spentTXOs[txID] { // 인덱스 값을 비교
						if spentOut == outIdx {
							continue Outputs
						}
					}
				}
				if out.IsLockedWithKey(pubKeyHash) {
					unspentTxs = append(unspentTxs, *tx)
				}
			}

			if !tx.IsCoinbase() {
				for _, in := range tx.Inputs {
					if in.UseKey(pubKeyHash) {
						inTxID := hex.EncodeToString(in.ID)
						spentTXOs[inTxID] = append(spentTXOs[inTxID], in.Out)
					}
				}
			}
		}
		if len(block.PrevHash) == 0 {
			break
		}
	}
	return unspentTxs
}

// 미사용 거래맵에서 미사용 출력을 찾기
func (chain *BlockChain) FindUTXO(pubKeyHash []byte) []TxOutput {
	var UTXOs []TxOutput
	unspentTransaction := chain.FindUnspentTransation(pubKeyHash)

	for _, tx := range unspentTransaction {
		for _, out := range tx.Outputs {
			if out.IsLockedWithKey(pubKeyHash) {
				UTXOs = append(UTXOs, out)
			}
		}
	}
	return UTXOs
}

// 미사용 출력을 이용해 amount 만큼 보낼수 있는지 확인
func (chain *BlockChain) FindSpendableOutputs(pubKeyHash []byte, amount int) (int, map[string][]int) {
	unspentOuts := make(map[string][]int)
	unspentTxs := chain.FindUnspentTransation(pubKeyHash)
	accumulated := 0

Work:
	for _, tx := range unspentTxs {
		txID := hex.EncodeToString(tx.ID)

		for outIdx, out := range tx.Outputs {
			if out.IsLockedWithKey(pubKeyHash) && accumulated < amount {
				accumulated += out.Value
				unspentOuts[txID] = append(unspentOuts[txID], outIdx)

				if accumulated >= amount {
					break Work
				}
			}
		}
	}

	return accumulated, unspentOuts
}

// TXID 를 통해서 거래 내역을 찾기 없으면 빈 거래와 에러를 반환
func (bc *BlockChain) FindTransaction(ID []byte) (Transaction, error) {
	iter := bc.Iterator()

	for {
		block := iter.Next()

		for _, tx := range block.Transactions {
			if bytes.Compare(tx.ID, ID) == 0 {
				return *tx, nil
			}
		}

		if len(block.PrevHash) == 0 {
			break
		}
	}
	return Transaction{}, errors.New("Transcation does not exist")
}

func (bc *BlockChain) SignTransaction(tx *Transaction, privKey ecdsa.PrivateKey) {
	prevTXs := make(map[string]Transaction)

	for _, in := range tx.Inputs {
		prevTX, err := bc.FindTransaction(in.ID)
		Handle(err)
		prevTXs[hex.EncodeToString(prevTX.ID)] = prevTX
	}
	tx.Sign(privKey, prevTXs)
}

func (bc *BlockChain) VerifyTransaction(tx *Transaction) bool {
	prevTXs := make(map[string]Transaction)

	for _, in := range tx.Inputs {
		prevTX, err := bc.FindTransaction(in.ID)
		Handle(err)
		prevTXs[hex.EncodeToString(prevTX.ID)] = prevTX
	}

	return tx.Verify(prevTXs)
}
