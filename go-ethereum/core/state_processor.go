// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"errors"
	"math/big"

	"github.com/boker/go-ethereum/bokerface"
	"github.com/boker/go-ethereum/common"
	"github.com/boker/go-ethereum/consensus"
	"github.com/boker/go-ethereum/consensus/misc"
	"github.com/boker/go-ethereum/core/state"
	"github.com/boker/go-ethereum/core/types"
	"github.com/boker/go-ethereum/core/vm"
	"github.com/boker/go-ethereum/crypto"
	"github.com/boker/go-ethereum/params"
)

//状态处理器，负责一个从一个节点到另一个节点
type StateProcessor struct {
	config *params.ChainConfig      //链配置选项
	bc     *BlockChain              //规范块链
	engine consensus.Engine         //共识引擎
	boker  bokerface.BokerInterface //播客链的接口
}

//初始化一个新的状态处理器。
func NewStateProcessor(config *params.ChainConfig, bc *BlockChain, engine consensus.Engine) *StateProcessor {
	return &StateProcessor{
		config: config,
		bc:     bc,
		engine: engine,
	}
}

func (p *StateProcessor) SetBoker(boker bokerface.BokerInterface) { p.boker = boker }

//根据以太坊的规则计算statedb的变化，并且将对应的收益转给coinbase和uncles，返回转账的收据，对应的日志和是否成功
func (p *StateProcessor) Process(block *types.Block,
	statedb *state.StateDB,
	cfg vm.Config) (types.Receipts, []*types.Log, *big.Int, error) {

	var (
		receipts     types.Receipts
		totalUsedGas = big.NewInt(0)
		header       = block.Header()
		allLogs      []*types.Log
		gp           = new(GasPool).AddGas(block.GasLimit())
	)

	//根据任何硬叉规范改变块和状态
	if p.config.DAOForkSupport && p.config.DAOForkBlock != nil && p.config.DAOForkBlock.Cmp(block.Number()) == 0 {
		misc.ApplyDAOHardFork(statedb)
	}

	//得到区块中所有的交易，并将这些交易使用Dpos引擎进行执行。
	for i, tx := range block.Transactions() {

		//设置当前statedb状态,以便后面evm创建交易日志
		statedb.Prepare(tx.Hash(), block.Hash(), i)
		receipt, _, err := ApplyTransaction(p.config, block.DposCtx(), p.bc, nil, gp, statedb, header, tx, totalUsedGas, cfg, p.boker)
		if err != nil {
			return nil, nil, nil, err
		}

		//执行完毕的交易回执放入到回执数组中
		receipts = append(receipts, receipt)
		allLogs = append(allLogs, receipt.Logs...)
	}

	//执行完块中所有的交易，应用任何共识引擎特定的附加功能（例如块奖励）
	p.engine.Finalize(p.bc, header, statedb, block.Transactions(), block.Uncles(), receipts, block.DposCtx())

	//返回执行成功的回执数组/日志/以及总的使用Gas的数量
	return receipts, allLogs, totalUsedGas, nil
}

//基础合约类型执行
func baseApplyTransaction(config *params.ChainConfig,
	dposContext *types.DposContext,
	bc *BlockChain,
	author *common.Address,
	gp *GasPool,
	statedb *state.StateDB,
	header *types.Header,
	tx *types.Transaction,
	usedGas *big.Int,
	cfg vm.Config,
	msg types.Message,
	boker bokerface.BokerInterface) (*types.Receipt, *big.Int, error) {

	msg, err := tx.AsMessage(types.MakeSigner(config, header.Number))
	if err != nil {
		return nil, nil, err
	}

	context := NewEVMContext(msg, header, bc, author)
	vmenv := vm.NewEVM(context, statedb, config, cfg)
	_, gas, failed, err := ApplyBaseMessage(vmenv, msg, gp)
	if err != nil {
		return nil, nil, err
	}

	if err = applyDposMessage(dposContext, msg); err != nil {
		return nil, nil, err
	}

	var root []byte
	if config.IsByzantium(header.Number) {
		statedb.Finalise(true)
	} else {

		root = statedb.IntermediateRoot(config.IsEIP158(header.Number)).Bytes()
	}
	usedGas.Add(usedGas, gas)

	receipt := types.NewReceipt(root, failed, usedGas)
	receipt.TxHash = tx.Hash()
	receipt.GasUsed = new(big.Int).Set(gas)
	if msg.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(vmenv.Context.Origin, tx.Nonce())
	}

	receipt.Logs = statedb.GetLogs(tx.Hash())
	receipt.Bloom = types.CreateBloom(types.Receipts{receipt})
	return receipt, gas, err
}

//设置部署基础交易
func setDeployTransaction(config *params.ChainConfig,
	dposContext *types.DposContext,
	bc *BlockChain,
	author *common.Address,
	gp *GasPool,
	statedb *state.StateDB,
	header *types.Header,
	tx *types.Transaction,
	usedGas *big.Int,
	cfg vm.Config,
	msg types.Message,
	boker bokerface.BokerInterface) (*types.Receipt, *big.Int, error) {

	msg, err := tx.AsMessage(types.MakeSigner(config, header.Number))
	if err != nil {
		return nil, nil, err
	}

	context := NewEVMContext(msg, header, bc, author)
	vmenv := vm.NewEVM(context, statedb, config, cfg)
	_, gas, failed, err := setDeployMessage(vmenv, msg, gp, msg.Type(), boker)
	if err != nil {
		return nil, nil, err
	}

	var root []byte
	if config.IsByzantium(header.Number) {
		statedb.Finalise(true)
	} else {

		root = statedb.IntermediateRoot(config.IsEIP158(header.Number)).Bytes()
	}
	usedGas.Add(usedGas, gas)

	receipt := types.NewReceipt(root, failed, usedGas)
	receipt.TxHash = tx.Hash()
	receipt.GasUsed = new(big.Int).Set(gas)
	if msg.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(vmenv.Context.Origin, tx.Nonce())
	}

	receipt.Logs = statedb.GetLogs(tx.Hash())
	receipt.Bloom = types.CreateBloom(types.Receipts{receipt})
	return receipt, gas, err
}

//取消设置部署基础交易
func cancelDeployTransaction(config *params.ChainConfig,
	dposContext *types.DposContext,
	bc *BlockChain,
	author *common.Address,
	gp *GasPool,
	statedb *state.StateDB,
	header *types.Header,
	tx *types.Transaction,
	usedGas *big.Int,
	cfg vm.Config,
	msg types.Message,
	boker bokerface.BokerInterface) (*types.Receipt, *big.Int, error) {

	msg, err := tx.AsMessage(types.MakeSigner(config, header.Number))
	if err != nil {
		return nil, nil, err
	}

	context := NewEVMContext(msg, header, bc, author)
	vmenv := vm.NewEVM(context, statedb, config, cfg)
	_, gas, failed, err := cancelDeployMessage(vmenv, msg, gp, msg.Type(), boker)
	if err != nil {
		return nil, nil, err
	}

	var root []byte
	if config.IsByzantium(header.Number) {
		statedb.Finalise(true)
	} else {

		root = statedb.IntermediateRoot(config.IsEIP158(header.Number)).Bytes()
	}
	usedGas.Add(usedGas, gas)

	receipt := types.NewReceipt(root, failed, usedGas)
	receipt.TxHash = tx.Hash()
	receipt.GasUsed = new(big.Int).Set(gas)
	if msg.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(vmenv.Context.Origin, tx.Nonce())
	}

	receipt.Logs = statedb.GetLogs(tx.Hash())
	receipt.Bloom = types.CreateBloom(types.Receipts{receipt})
	return receipt, gas, err
}

//投票合约合约
func voteTransaction(config *params.ChainConfig,
	dposContext *types.DposContext,
	bc *BlockChain,
	author *common.Address,
	gp *GasPool,
	statedb *state.StateDB,
	header *types.Header,
	tx *types.Transaction,
	usedGas *big.Int,
	cfg vm.Config,
	msg types.Message,
	boker bokerface.BokerInterface) (*types.Receipt, *big.Int, error) {

	msg, err := tx.AsMessage(types.MakeSigner(config, header.Number))
	if err != nil {
		return nil, nil, err
	}

	context := NewEVMContext(msg, header, bc, author)
	vmenv := vm.NewEVM(context, statedb, config, cfg)
	_, gas, failed, err := voteMessage(vmenv, msg, gp, msg.Type(), boker)
	if err != nil {
		return nil, nil, err
	}

	var root []byte
	if config.IsByzantium(header.Number) {
		statedb.Finalise(true)
	} else {

		root = statedb.IntermediateRoot(config.IsEIP158(header.Number)).Bytes()
	}
	usedGas.Add(usedGas, gas)

	receipt := types.NewReceipt(root, failed, usedGas)
	receipt.TxHash = tx.Hash()
	receipt.GasUsed = new(big.Int).Set(gas)
	if msg.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(vmenv.Context.Origin, tx.Nonce())
	}

	receipt.Logs = statedb.GetLogs(tx.Hash())
	receipt.Bloom = types.CreateBloom(types.Receipts{receipt})
	return receipt, gas, err
}

//通证分配相关合约类型执行
func tokenTransaction(config *params.ChainConfig,
	dposContext *types.DposContext,
	bc *BlockChain,
	author *common.Address,
	gp *GasPool,
	statedb *state.StateDB,
	header *types.Header,
	tx *types.Transaction,
	usedGas *big.Int,
	cfg vm.Config,
	msg types.Message,
	boker bokerface.BokerInterface) (*types.Receipt, *big.Int, error) {

	context := NewEVMContext(msg, header, bc, author)
	vmenv := vm.NewEVM(context, statedb, config, cfg)
	tokenNoder, err := dposContext.GetCurrentTokenNoder()
	if tokenNoder != msg.From() {
		return nil, nil, errors.New("from address not assign token producer")
	}

	_, gas, failed, err := tokenMessage(vmenv, msg, gp, msg.Type(), boker)
	if err != nil {
		return nil, nil, err
	}

	var root []byte
	if config.IsByzantium(header.Number) {
		statedb.Finalise(true)
	} else {
		root = statedb.IntermediateRoot(config.IsEIP158(header.Number)).Bytes()
	}
	usedGas.Add(usedGas, gas)

	receipt := types.NewReceipt(root, failed, usedGas)
	receipt.TxHash = tx.Hash()
	receipt.GasUsed = new(big.Int).Set(gas)
	if msg.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(vmenv.Context.Origin, tx.Nonce())
	}

	receipt.Logs = statedb.GetLogs(tx.Hash())
	receipt.Bloom = types.CreateBloom(types.Receipts{receipt})

	return receipt, gas, err
}

//处理普通交易
func binaryApplyTransaction(config *params.ChainConfig,
	dposContext *types.DposContext,
	bc *BlockChain,
	author *common.Address,
	gp *GasPool,
	statedb *state.StateDB,
	header *types.Header,
	tx *types.Transaction,
	usedGas *big.Int,
	cfg vm.Config,
	msg types.Message,
	boker bokerface.BokerInterface) (*types.Receipt, *big.Int, error) {

	msg, err := tx.AsMessage(types.MakeSigner(config, header.Number))
	if err != nil {
		return nil, nil, err
	}

	context := NewEVMContext(msg, header, bc, author)
	vmenv := vm.NewEVM(context, statedb, config, cfg)
	_, gas, failed, err := ApplyMessage(vmenv, msg, gp)
	if err != nil {
		return nil, nil, err
	}

	//用待处理的更改更新状态
	var root []byte
	if config.IsByzantium(header.Number) {
		statedb.Finalise(true)
	} else {
		root = statedb.IntermediateRoot(config.IsEIP158(header.Number)).Bytes()
	}
	usedGas.Add(usedGas, gas)

	//为交易创建一个新收据，存储tx使用的中间根和gas基于eip阶段，我们传递了根触发删除帐户。
	receipt := types.NewReceipt(root, failed, usedGas)
	receipt.TxHash = tx.Hash()
	receipt.GasUsed = new(big.Int).Set(gas)

	//如果交易创建了合同，则将创建地址存储在收据中
	if msg.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(vmenv.Context.Origin, tx.Nonce())
	}

	//设置收据日志并创建一个用于过滤的布尔值
	receipt.Logs = statedb.GetLogs(tx.Hash())
	receipt.Bloom = types.CreateBloom(types.Receipts{receipt})

	return receipt, gas, err
}

//执行交易
func ApplyTransaction(config *params.ChainConfig,
	dposContext *types.DposContext,
	bc *BlockChain,
	author *common.Address,
	gp *GasPool,
	statedb *state.StateDB,
	header *types.Header,
	tx *types.Transaction,
	usedGas *big.Int,
	cfg vm.Config,
	boker bokerface.BokerInterface) (*types.Receipt, *big.Int, error) {

	msg, err := tx.AsMessage(types.MakeSigner(config, header.Number))
	if err != nil {
		return nil, nil, err
	}

	//根据交易类型来区分
	switch msg.Type() {

	case types.Binary: //普通交易

		return binaryApplyTransaction(config, dposContext, bc, author, gp, statedb, header, tx, usedGas, cfg, msg, boker)
	case types.DeployVote, types.DeployAssignToken: //设置部署基础交易

		return setDeployTransaction(config, dposContext, bc, author, gp, statedb, header, tx, usedGas, cfg, msg, boker)

	case types.UnDeployVote, types.UnDeployAssignToken: //取消设置部署基础交易

		return cancelDeployTransaction(config, dposContext, bc, author, gp, statedb, header, tx, usedGas, cfg, msg, boker)
	case types.RegisterCandidate, types.ProducerVote, types.RotateVote: //投票交易

		return voteTransaction(config, dposContext, bc, author, gp, statedb, header, tx, usedGas, cfg, msg, boker)
	case types.AssignToken, types.ProducerReward: //通证分配交易

		return tokenTransaction(config, dposContext, bc, author, gp, statedb, header, tx, usedGas, cfg, msg, boker)
	default:

		return nil, nil, types.ErrInvalidType
	}
}

//这里是将两个基础合约的信息写入到基础链的Hash树中
func applyDposMessage(dposContext *types.DposContext, msg types.Message) error {

	//根据基础合约类型来进行区分
	/*switch msg.Type() {
	case types.RegisterCandidate: //成为候选人
		dposContext.BecomeCandidate(msg.From())
	case types.VoteCandidate: //投票（授权）
		dposContext.Delegate(msg.From(), *(msg.To()))
	default:
		return types.ErrInvalidType
	}*/
	return nil
}