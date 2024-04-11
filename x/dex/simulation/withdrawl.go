package simulation

import (
	"math/rand"

	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"

	"github.com/neutron-org/neutron/v3/x/dex/keeper"
	"github.com/neutron-org/neutron/v3/x/dex/types"
)

func SimulateMsgWithdrawal(
	_ types.BankKeeper,
	_ keeper.Keeper,
) simtypes.Operation {
	return func(r *rand.Rand, app *baseapp.BaseApp, ctx sdk.Context, accs []simtypes.Account, chainID string,
	) (simtypes.OperationMsg, []simtypes.FutureOperation, error) {
		simAccount, _ := simtypes.RandomAcc(r, accs)
		msg := &types.MsgWithdrawal{
			Creator: simAccount.Address.String(),
		}

		// TODO: Handling the Withdrawal simulation

		return simtypes.NoOpMsg(types.ModuleName, msg.Type(), "Withdrawal simulation not implemented"), nil, nil
	}
}
