package blocks

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/OffchainLabs/prysm/v6/beacon-chain/state"
	"github.com/OffchainLabs/prysm/v6/config/params"
	ethpb "github.com/OffchainLabs/prysm/v6/proto/prysm/v1alpha1"
	"github.com/sirupsen/logrus"
)

// ProcessEth1DataInBlock is an operation performed on each
// beacon block to ensure the ETH1 data votes are processed
// into the beacon state.
//
// Official spec definition:
//
//	def process_eth1_data(state: BeaconState, body: BeaconBlockBody) -> None:
//	 state.eth1_data_votes.append(body.eth1_data)
//	 if state.eth1_data_votes.count(body.eth1_data) * 2 > EPOCHS_PER_ETH1_VOTING_PERIOD * SLOTS_PER_EPOCH:
//	     state.eth1_data = body.eth1_data
func ProcessEth1DataInBlock(_ context.Context, beaconState state.BeaconState, eth1Data *ethpb.Eth1Data) (state.BeaconState, error) {
	if beaconState == nil || beaconState.IsNil() {
		return nil, errors.New("nil state")
	}
	if err := beaconState.AppendEth1DataVotes(eth1Data); err != nil {
		return nil, err
	}
	
	// Log eth1_data_votes state before checking for majority
	currentSlot := beaconState.Slot()
	votingPeriodSlots := params.BeaconConfig().SlotsPerEpoch.Mul(uint64(params.BeaconConfig().EpochsPerEth1VotingPeriod))
	currentPeriodStartSlot := (currentSlot / votingPeriodSlots) * votingPeriodSlots
	voteDetails := make(map[string]struct {
		count        int
		depositCount uint64
		blockHash    []byte
	})
	totalVotes := 0
	
	for _, vote := range beaconState.Eth1DataVotes() {
		key := fmt.Sprintf("%#x", vote.BlockHash)
		if details, exists := voteDetails[key]; exists {
			details.count++
			voteDetails[key] = details
		} else {
			voteDetails[key] = struct {
				count        int
				depositCount uint64
				blockHash    []byte
			}{
				count:        1,
				depositCount: vote.DepositCount,
				blockHash:    vote.BlockHash,
			}
		}
		totalVotes++
	}
	
	// Find top 3 most voted Eth1Data
	type voteInfo struct {
		blockHash    string
		count        int
		depositCount uint64
	}
	var topVotes []voteInfo
	for hash, details := range voteDetails {
		topVotes = append(topVotes, voteInfo{
			blockHash:    hash,
			count:        details.count,
			depositCount: details.depositCount,
		})
	}
	// Sort by vote count descending
	if len(topVotes) > 1 {
		for i := 0; i < len(topVotes)-1; i++ {
			for j := i + 1; j < len(topVotes); j++ {
				if topVotes[j].count > topVotes[i].count {
					topVotes[i], topVotes[j] = topVotes[j], topVotes[i]
				}
			}
		}
	}
	
	// Log top 3 votes
	topVotesLog := make([]string, 0, 3)
	for i := 0; i < len(topVotes) && i < 3; i++ {
		topVotesLog = append(topVotesLog, fmt.Sprintf("%s(votes:%d,deposits:%d)", 
			topVotes[i].blockHash, topVotes[i].count, topVotes[i].depositCount))
	}
	
	log.WithFields(logrus.Fields{
		"slot":                   currentSlot,
		"votingPeriodStartSlot":  currentPeriodStartSlot,
		"votingPeriodEndSlot":    currentPeriodStartSlot + votingPeriodSlots - 1,
		"slotInPeriod":           currentSlot - currentPeriodStartSlot,
		"totalVotes":             totalVotes,
		"uniqueEth1DataVotes":    len(voteDetails),
		"newVoteBlockHash":       fmt.Sprintf("%#x", eth1Data.BlockHash),
		"newVoteDepositCount":    eth1Data.DepositCount,
		"topVotes":               topVotesLog,
	}).Debug("Eth1Data vote added to state")
	
	hasSupport, err := Eth1DataHasEnoughSupport(beaconState, eth1Data)
	if err != nil {
		return nil, err
	}
	if hasSupport {
		if err := beaconState.SetEth1Data(eth1Data); err != nil {
			return nil, err
		}
		log.WithFields(logrus.Fields{
			"slot":            beaconState.Slot(),
			"eth1BlockHash":   fmt.Sprintf("%#x", eth1Data.BlockHash),
			"eth1DepositRoot": fmt.Sprintf("%#x", eth1Data.DepositRoot),
			"eth1DepositCount": eth1Data.DepositCount,
		}).Debug("BeaconState Eth1Data updated with majority vote")
	}
	return beaconState, nil
}

// AreEth1DataEqual checks equality between two eth1 data objects.
func AreEth1DataEqual(a, b *ethpb.Eth1Data) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.DepositCount == b.DepositCount &&
		bytes.Equal(a.BlockHash, b.BlockHash) &&
		bytes.Equal(a.DepositRoot, b.DepositRoot)
}

// Eth1DataHasEnoughSupport returns true when the given eth1data has more than 50% votes in the
// eth1 voting period. A vote is cast by including eth1data in a block and part of state processing
// appends eth1data to the state in the Eth1DataVotes list. Iterating through this list checks the
// votes to see if they match the eth1data.
func Eth1DataHasEnoughSupport(beaconState state.ReadOnlyBeaconState, data *ethpb.Eth1Data) (bool, error) {
	voteCount := uint64(0)
	totalVotes := uint64(len(beaconState.Eth1DataVotes()))

	for _, vote := range beaconState.Eth1DataVotes() {
		if AreEth1DataEqual(vote, data.Copy()) {
			voteCount++
		}
	}

	// If 50+% majority converged on the same eth1data, then it has enough support to update the
	// state.
	votingPeriodSlots := params.BeaconConfig().SlotsPerEpoch.Mul(uint64(params.BeaconConfig().EpochsPerEth1VotingPeriod))
	requiredVotes := uint64(votingPeriodSlots) / 2
	hasSupport := voteCount*2 > uint64(votingPeriodSlots)
	
	// Log the majority check details
	log.WithFields(logrus.Fields{
		"eth1BlockHash":      fmt.Sprintf("%#x", data.BlockHash),
		"eth1DepositCount":   data.DepositCount,
		"votesForThis":       voteCount,
		"totalVotes":         totalVotes,
		"votingPeriodSlots":  votingPeriodSlots,
		"requiredVotes":      requiredVotes + 1, // +1 because we need >50%, not >=50%
		"hasSupport":         hasSupport,
		"percentageSupport":  fmt.Sprintf("%.2f%%", float64(voteCount)*100/float64(votingPeriodSlots)),
	}).Debug("Eth1Data majority check")
	
	return hasSupport, nil
}
