/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/
package transfer_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/pkg/errors"

	"github.com/hyperledger-labs/fabric-token-sdk/token/core/math/gurvy/bn256"
	"github.com/hyperledger-labs/fabric-token-sdk/token/core/zkatdlog/crypto"
	"github.com/hyperledger-labs/fabric-token-sdk/token/core/zkatdlog/crypto/token"
	transfer2 "github.com/hyperledger-labs/fabric-token-sdk/token/core/zkatdlog/crypto/transfer"
	"github.com/hyperledger-labs/fabric-token-sdk/token/core/zkatdlog/crypto/transfer/mock"
	"github.com/hyperledger-labs/fabric-token-sdk/token/driver"
)

var _ = Describe("Sender", func() {
	var (
		fakeSigningIdentity *mock.SigningIdentity
		signers             []driver.Signer
		pp                  *crypto.PublicParams

		transfer *transfer2.TransferAction
		sender   *transfer2.Sender

		invalues  []*bn256.Zr
		outvalues []uint64
		inBF      []*bn256.Zr
		tokens    []*token.Token

		owners [][]byte
		ids    []string
	)
	BeforeEach(func() {
		var err error
		owners = make([][]byte, 2)
		owners[0] = []byte("bob")
		owners[1] = []byte("charlie")
		signers = make([]driver.Signer, 3)
		fakeSigningIdentity = &mock.SigningIdentity{}
		signers[0] = fakeSigningIdentity
		signers[1] = fakeSigningIdentity
		signers[2] = fakeSigningIdentity

		fakeSigningIdentity.SignReturnsOnCall(0, []byte("signer[0]"), nil)
		fakeSigningIdentity.SignReturnsOnCall(1, []byte("signer[1]"), nil)
		fakeSigningIdentity.SignReturnsOnCall(2, []byte("signer[2]"), nil)

		invalues = make([]*bn256.Zr, 3)
		invalues[0] = bn256.NewZrInt(50)
		invalues[1] = bn256.NewZrInt(20)
		invalues[2] = bn256.NewZrInt(30)

		inBF = make([]*bn256.Zr, 3)
		rand, err := bn256.GetRand()
		Expect(err).NotTo(HaveOccurred())
		for i := 0; i < 3; i++ {
			inBF[i] = bn256.RandModOrder(rand)
		}
		outvalues = make([]uint64, 2)
		outvalues[0] = 65
		outvalues[1] = 35

		ids = make([]string, 3)
		ids[0] = "0"
		ids[1] = "1"
		ids[2] = "3"

		pp, err = crypto.Setup(100, 2, nil)
		Expect(err).NotTo(HaveOccurred())

		inputs := PrepareTokens(invalues, inBF, "ABC", pp.ZKATPedParams)
		tokens = make([]*token.Token, 3)

		tokens[0] = &token.Token{Data: inputs[0], Owner: []byte("alice-1")}
		tokens[1] = &token.Token{Data: inputs[1], Owner: []byte("alice-2")}
		tokens[2] = &token.Token{Data: inputs[2], Owner: []byte("alice-3")}

		inputInf := make([]*token.TokenInformation, 3)
		inputInf[0] = &token.TokenInformation{Type: "ABC", Value: invalues[0], BlindingFactor: inBF[0]}
		inputInf[1] = &token.TokenInformation{Type: "ABC", Value: invalues[1], BlindingFactor: inBF[1]}
		inputInf[2] = &token.TokenInformation{Type: "ABC", Value: invalues[2], BlindingFactor: inBF[2]}

		sender, err = transfer2.NewSender(signers, tokens, ids, inputInf, pp)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("Transfer", func() {
		When("transfer is computed correctly", func() {
			It("succeeds", func() {
				var err error
				transfer, _, err = sender.GenerateZKTransfer(outvalues, owners)
				Expect(err).NotTo(HaveOccurred())
				Expect(transfer).NotTo(BeNil())
				raw, err := transfer.Serialize()
				Expect(err).NotTo(HaveOccurred())

				sig, err := sender.SignTokenActions(raw, "0")
				Expect(fakeSigningIdentity.SignCallCount()).To(Equal(3))
				Expect(len(sig)).To(Equal(3))
				Expect(err).NotTo(HaveOccurred())
			})
		})
		When("when signature fails", func() {
			BeforeEach(func() {
				fakeSigningIdentity.SignReturnsOnCall(2, nil, errors.New("banana republic"))
			})
			It("no signature is returned", func() {
				var err error
				transfer, _, err = sender.GenerateZKTransfer(outvalues, owners)
				Expect(err).NotTo(HaveOccurred())
				Expect(transfer).NotTo(BeNil())
				raw, err := transfer.Serialize()
				Expect(err).NotTo(HaveOccurred())

				sig, err := sender.SignTokenActions(raw, "0")
				Expect(err).To(HaveOccurred())
				Expect(sig).To(BeNil())
				Expect(fakeSigningIdentity.SignCallCount()).To(Equal(3))
				Expect(err.Error()).To(ContainSubstring("banana republic"))
			})
		})
	})
})

func PrepareTokens(values, bf []*bn256.Zr, ttype string, pp []*bn256.G1) []*bn256.G1 {
	tokens := make([]*bn256.G1, len(values))
	for i := 0; i < len(values); i++ {
		tokens[i] = prepareToken(values[i], bf[i], ttype, pp)
	}
	return tokens
}
