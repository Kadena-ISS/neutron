package keeper

import (
	"fmt"
	"time"

	"cosmossdk.io/errors"

	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	channeltypes "github.com/cosmos/ibc-go/v7/modules/core/04-channel/types"

	contractmanagertypes "github.com/neutron-org/neutron/x/contractmanager/types"
	feetypes "github.com/neutron-org/neutron/x/feerefunder/types"
	"github.com/neutron-org/neutron/x/interchaintxs/types"
)

// HandleAcknowledgement passes the acknowledgement data to the appropriate contract via a Sudo call.
func (k *Keeper) HandleAcknowledgement(ctx sdk.Context, packet channeltypes.Packet, acknowledgement []byte, relayer sdk.AccAddress) error {
	defer telemetry.ModuleMeasureSince(types.ModuleName, time.Now(), LabelHandleAcknowledgment)

	k.Logger(ctx).Debug("Handling acknowledgement")
	icaOwner, err := types.ICAOwnerFromPort(packet.SourcePort)
	if err != nil {
		k.Logger(ctx).Error("HandleAcknowledgement: failed to get ica owner from source port", "error", err)
		return errors.Wrap(err, "failed to get ica owner from port")
	}

	var ack channeltypes.Acknowledgement
	if err := channeltypes.SubModuleCdc.UnmarshalJSON(acknowledgement, &ack); err != nil {
		k.Logger(ctx).Error("HandleAcknowledgement: cannot unmarshal ICS-27 packet acknowledgement", "error", err)
		return errors.Wrapf(sdkerrors.ErrUnknownRequest, "cannot unmarshal ICS-27 packet acknowledgement: %v", err)
	}

	cacheCtx, writeFn, newGasMeter := k.createCachedContext(ctx)

	k.feeKeeper.DistributeAcknowledgementFee(ctx, relayer, feetypes.NewPacketID(packet.SourcePort, packet.SourceChannel, packet.Sequence))

	func() {
		// Actually we have only one kind of error returned from acknowledgement
		// maybe later we'll retrieve actual errors from events
		errorText := ack.GetError()
		if errorText != "" {
			_, err = k.contractManagerKeeper.SudoError(cacheCtx, icaOwner.GetContract(), packet, errorText)
		} else {
			_, err = k.contractManagerKeeper.SudoResponse(cacheCtx, icaOwner.GetContract(), packet, ack.GetResult())
		}
		defer k.outOfGasRecovery(ctx, newGasMeter, icaOwner.GetContract(), packet, "ack")
	}()
	// consume the gas from the cached context
	ctx.GasMeter().ConsumeGas(newGasMeter.GasConsumed(), "consume gas from cached context")

	if err != nil {
		k.contractManagerKeeper.AddContractFailure(ctx, packet.SourceChannel, icaOwner.GetContract().String(), packet.GetSequence(), "ack")
		k.Logger(ctx).Debug("HandleAcknowledgement: failed to Sudo contract on packet acknowledgement", "error", err)
	} else {
		writeFn()
	}

	return nil
}

// HandleTimeout passes the timeout data to the appropriate contract via a Sudo call.
// Since all ICA channels are ORDERED, a single timeout shuts down a channel.
func (k *Keeper) HandleTimeout(ctx sdk.Context, packet channeltypes.Packet, relayer sdk.AccAddress) error {
	defer telemetry.ModuleMeasureSince(types.ModuleName, time.Now(), LabelHandleTimeout)

	icaOwner, err := types.ICAOwnerFromPort(packet.SourcePort)
	k.Logger(ctx).Debug("HandleTimeout")
	if err != nil {
		k.Logger(ctx).Error("HandleTimeout: failed to get ica owner from source port", "error", err)
		return errors.Wrap(err, "failed to get ica owner from port")
	}

	cacheCtx, writeFn, newGasMeter := k.createCachedContext(ctx)
	// consume all the gas from the cached context
	// we call the function this place function because we want to consume all the gas even in case of panic in SudoTimeout
	ctx.GasMeter().ConsumeGas(newGasMeter.Limit(), "consume full gas from cached context")
	defer k.outOfGasRecovery(ctx, newGasMeter, icaOwner.GetContract(), packet, "timeout")

	k.feeKeeper.DistributeTimeoutFee(ctx, relayer, feetypes.NewPacketID(packet.SourcePort, packet.SourceChannel, packet.Sequence))

	_, err = k.contractManagerKeeper.SudoTimeout(cacheCtx, icaOwner.GetContract(), packet)
	if err != nil {
		k.contractManagerKeeper.AddContractFailure(ctx, packet.SourceChannel, icaOwner.GetContract().String(), packet.GetSequence(), "timeout")
		k.Logger(ctx).Error("HandleTimeout: failed to Sudo contract on packet timeout", "error", err)
	} else {
		writeFn()
	}

	return nil
}

// HandleChanOpenAck passes the data about a successfully created channel to the appropriate contract
// (== the data about a successfully registered interchain account).
// Notice that in the case of an ICA channel - it is not yet in OPEN state here
// the last step of channel opening(confirm) happens on the host chain.
func (k *Keeper) HandleChanOpenAck(
	ctx sdk.Context,
	portID,
	channelID,
	counterpartyChannelID,
	counterpartyVersion string,
) error {
	defer telemetry.ModuleMeasureSince(types.ModuleName, time.Now(), LabelLabelHandleChanOpenAck)

	k.Logger(ctx).Debug("HandleChanOpenAck", "port_id", portID, "channel_id", channelID, "counterparty_channel_id", counterpartyChannelID, "counterparty_version", counterpartyVersion)
	icaOwner, err := types.ICAOwnerFromPort(portID)
	if err != nil {
		k.Logger(ctx).Error("HandleChanOpenAck: failed to get ica owner from source port", "error", err)
		return errors.Wrap(err, "failed to get ica owner from port")
	}

	_, err = k.contractManagerKeeper.SudoOnChanOpenAck(ctx, icaOwner.GetContract(), contractmanagertypes.OpenAckDetails{
		PortID:                portID,
		ChannelID:             channelID,
		CounterpartyChannelID: counterpartyChannelID,
		CounterpartyVersion:   counterpartyVersion,
	})
	if err != nil {
		k.Logger(ctx).Debug("HandleChanOpenAck: failed to Sudo contract on packet timeout", "error", err)
		return errors.Wrap(err, "failed to Sudo the contract OnChanOpenAck")
	}

	return nil
}

func (k *Keeper) outOfGasRecovery(
	ctx sdk.Context,
	gasMeter sdk.GasMeter,
	senderAddress sdk.AccAddress,
	packet channeltypes.Packet,
	failureAckType string,
) {
	if r := recover(); r != nil {
		_, ok := r.(sdk.ErrorOutOfGas)
		if !ok || !gasMeter.IsOutOfGas() {
			panic(r)
		}

		k.Logger(ctx).Debug("Out of gas", "Gas meter", gasMeter.String())
		k.contractManagerKeeper.AddContractFailure(ctx, packet.SourceChannel, senderAddress.String(), packet.GetSequence(), failureAckType)
	}
}

// createCachedContext creates a cached context for handling Sudo calls to CosmWasm smart-contracts.
// If there is an error during Sudo call, we can safely revert changes made in cached context.
// panics if there is no enough gas for sudoCall
func (k *Keeper) createCachedContext(ctx sdk.Context) (sdk.Context, func(), sdk.GasMeter) {
	cacheCtx, writeFn := ctx.CacheContext()

	sudoLimit := k.contractManagerKeeper.GetParams(ctx).SudoCallGasLimit
	// NOTE: not sure that we really need this special check and panic
	// with this kind of panic its clear what is going on by error text
	// with this check, handle flow is not changed, but we get general panic during
	// call ctx.GasMeter().ConsumeGas(newGasMeter.Limit(), "consume full gas from cached context")
	if ctx.GasMeter().GasRemaining() < sudoLimit {
		panic(sdk.ErrorOutOfGas{Descriptor: fmt.Sprintf("%dgas - reserve for sudo call", sudoLimit)})
	}

	gasMeter := sdk.NewGasMeter(sudoLimit)

	cacheCtx = cacheCtx.WithGasMeter(gasMeter)

	return cacheCtx, writeFn, gasMeter
}
