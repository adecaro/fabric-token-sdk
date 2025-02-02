/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/
package processor

import (
	"strconv"

	"github.com/hyperledger-labs/fabric-smart-client/platform/fabric"
	view2 "github.com/hyperledger-labs/fabric-smart-client/platform/view"
	"github.com/hyperledger-labs/fabric-smart-client/platform/view/services/flogging"
	"github.com/pkg/errors"

	"github.com/hyperledger-labs/fabric-token-sdk/token"
	"github.com/hyperledger-labs/fabric-token-sdk/token/services/vault/keys"
)

var logger = flogging.MustGetLogger("token-sdk.vault.processor")

type Network interface {
	Channel(id string) (*fabric.Channel, error)
}

type RWSetProcessor struct {
	network Network
	nss     []string
	sp      view2.ServiceProvider
}

func NewTokenRWSetProcessor(network Network, ns string, sp view2.ServiceProvider) *RWSetProcessor {
	return &RWSetProcessor{
		network: network,
		nss:     []string{ns},
		sp:      sp}
}

func (r *RWSetProcessor) Process(req fabric.Request, tx fabric.ProcessTransaction, rws *fabric.RWSet, ns string) error {
	found := false
	for _, ans := range r.nss {
		if ns == ans {
			found = true
			break
		}
	}
	if !found {
		return errors.Errorf("this processor cannot parse namespace [%s]", ns)
	}

	fn, _ := tx.FunctionAndParameters()
	logger.Debugf("process namespace and function [%s:%s]", ns, fn)
	switch fn {
	case "setup":
		return r.setup(req, tx, rws, ns)
	default:
		return r.tokenRequest(req, tx, rws, ns)
	}
}

func (r *RWSetProcessor) setup(req fabric.Request, tx fabric.ProcessTransaction, rws *fabric.RWSet, ns string) error {
	logger.Debugf("[setup] store setup bundle")
	key, err := keys.CreateSetupBundleKey()
	if err != nil {
		return err
	}
	logger.Debugf("[setup] store setup bundle [%s,%s]", key, req.ID())
	err = rws.SetState(ns, key, []byte(req.ID()))
	if err != nil {
		logger.Errorf("failed setting setup bundle state [%s,%s]", key, req.ID())
		return errors.Wrapf(err, "failed setting setup bundle state [%s,%s]", key, req.ID())
	}
	logger.Debugf("[setup] store setup bundle done")

	return nil
}

func (r *RWSetProcessor) tokenRequest(req fabric.Request, tx fabric.ProcessTransaction, rws *fabric.RWSet, ns string) error {
	txID := tx.ID()

	ch, err := r.network.Channel(tx.Channel())
	if err != nil {
		return errors.Wrapf(err, "failed getting channel [%s]", tx.Channel())
	}
	if !ch.MetadataService().Exists(txID) {
		logger.Debugf("transaction [%s] is not known to this node, no need to extract tokens", txID)
		return nil
	}

	logger.Debugf("transaction [%s] is known, extract tokens", txID)
	logger.Debugf("transaction [%s], parsing writes [%d]", txID, rws.NumWrites(ns))
	transientMap, err := ch.MetadataService().LoadTransient(txID)
	if err != nil {
		logger.Debugf("transaction [%s], failed getting transient map", txID)
		return err
	}
	if !transientMap.Exists("zkat") {
		logger.Debugf("transaction [%s], no transient map found", txID)
		return nil
	}

	tms := token.GetManagementService(
		r.sp,
		token.WithNetwork(tx.Network()),
		token.WithChannel(tx.Channel()),
		token.WithNamespace(ns),
	)
	metadata, err := tms.NewMetadataFromBytes(transientMap.Get("zkat"))
	if err != nil {
		logger.Debugf("transaction [%s], failed getting zkat state from transient map [%s]", txID, err)
		return err
	}

	if tms.PublicParametersManager().GraphHiding() {
		// Delete inputs
		for _, id := range metadata.SpentTokenID() {
			if err := r.deleteFabToken(ns, id.TxId, int(id.Index), rws); err != nil {
				return err
			}
		}
	}

	for i := 0; i < rws.NumWrites(ns); i++ {
		key, val, err := rws.GetWriteAt(ns, i)
		if err != nil {
			return err
		}
		logger.Debugf("Parsing write key [%s]", key)
		prefix, components, err := keys.SplitCompositeKey(key)
		if err != nil {
			panic(err)
		}
		if prefix != keys.TokenKeyPrefix {
			logger.Debugf("expected prefix [%s], got [%s], skipping", keys.TokenKeyPrefix, prefix)
			continue
		}
		switch components[0] {
		case keys.TokenMineKeyPrefix:
			logger.Debugf("expected key without the mine prefix, skipping")
			continue
		case keys.TokenRequestKeyPrefix:
			logger.Debugf("expected key without the token request prefix, skipping")
			continue
		case keys.SerialNumber:
			logger.Debugf("expected key without the serial number prefix, skipping")
			continue
		}

		index, err := strconv.Atoi(components[1])
		if err != nil {
			logger.Errorf("invalid output index for key [%s]", key)
			return errors.Wrapf(err, "invalid output index for key [%s]", key)
		}

		// This is a delete, add a delete for fabtoken
		if len(val) == 0 {
			if err := r.deleteFabToken(ns, components[0], index, rws); err != nil {
				return err
			}
			continue
		}

		if components[0] != txID {
			logger.Errorf("invalid output, must refer to tx id [%s], got [%s]", txID, components[0])
			return errors.Errorf("invalid output, must refer to tx id [%s], got [%s]", txID, components[0])
		}
		logger.Debugf("transaction [%s], found a token...", txID)

		// get token in the clear
		tok, issuer, tokenInfoRaw, err := metadata.GetToken(val)
		if err != nil {
			logger.Errorf("transaction [%s], found a token but failed getting the clear version, skipping it [%s]", txID, err)
			continue
		}
		if tok == nil {
			logger.Warnf("failed getting token in the clear for key [%s, %s]", key, string(val))
			continue
		}

		if tms.WalletManager().OwnerWalletByIdentity(tok.Owner.Raw) != nil {
			logger.Debugf("transaction [%s], found a token and it is mine", txID)
			// Add a lookup key to identity quickly that this token belongs to this
			mineTokenID, err := keys.CreateTokenMineKey(components[0], index)
			if err != nil {
				return errors.Wrapf(err, "failed computing mine key for for key [%s]", key)
			}
			err = rws.SetState(ns, mineTokenID, []byte{1})
			if err != nil {
				return err
			}

			// Store Fabtoken-like entry
			if err := r.storeFabToken(ns, txID, index, tok, rws, tokenInfoRaw); err != nil {
				return err
			}
		} else {
			logger.Debugf("transaction [%s], found a token and I must be the auditor", txID)
			if err := r.storeAuditToken(ns, txID, index, tok, rws, tokenInfoRaw); err != nil {
				return err
			}
		}

		if !issuer.IsNone() && tms.WalletManager().IssuerWalletByIdentity(issuer) != nil {
			logger.Debugf("transaction [%s], found a token and I have issued it", txID)
			if err := r.storeIssuedHistoryToken(ns, txID, index, tok, rws, tokenInfoRaw, issuer); err != nil {
				return err
			}
		}

		logger.Debugf("Done parsing write key [%s]", key)
	}
	logger.Debugf("transaction [%s] is known, extract tokens, done!", txID)

	return nil
}
