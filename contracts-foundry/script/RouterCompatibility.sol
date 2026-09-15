// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface IV2RouterMetadata {
    function WETH() external view returns (address);

    function factory() external view returns (address);
}

library RouterCompatibility {
    bytes4 internal constant ADD_LIQUIDITY_ETH_SELECTOR =
        bytes4(keccak256("addLiquidityETH(address,uint256,uint256,uint256,address,uint256)"));

    error RouterAddressZero();
    error RouterHasNoCode(address router);
    error RouterMissingAddLiquidityETH(address router);
    error RouterWethLookupFailed(address router);
    error RouterWethAddressZero(address router);
    error RouterFactoryLookupFailed(address router);
    error RouterFactoryAddressZero(address router);

    function requireV2Compatible(address router) internal view {
        if (router == address(0)) revert RouterAddressZero();

        bytes memory runtimeCode = router.code;
        if (runtimeCode.length == 0) revert RouterHasNoCode(router);

        // The launchpad graduates through native ETH, so the project's ERC20-only Router must
        // never be accepted merely because it exposes matching WETH and factory metadata.
        if (!_containsSelector(runtimeCode, ADD_LIQUIDITY_ETH_SELECTOR)) {
            revert RouterMissingAddLiquidityETH(router);
        }

        address weth;
        try IV2RouterMetadata(router).WETH() returns (address configuredWeth) {
            weth = configuredWeth;
        } catch {
            revert RouterWethLookupFailed(router);
        }
        if (weth == address(0)) revert RouterWethAddressZero(router);

        address factory;
        try IV2RouterMetadata(router).factory() returns (address configuredFactory) {
            factory = configuredFactory;
        } catch {
            revert RouterFactoryLookupFailed(router);
        }
        if (factory == address(0)) revert RouterFactoryAddressZero(router);
    }

    function _containsSelector(
        bytes memory runtimeCode,
        bytes4 selector
    )
        private
        pure
        returns (bool)
    {
        if (runtimeCode.length < 4) return false;

        for (uint256 index; index <= runtimeCode.length - 4; ++index) {
            if (
                runtimeCode[index] == selector[0] && runtimeCode[index + 1] == selector[1]
                    && runtimeCode[index + 2] == selector[2]
                    && runtimeCode[index + 3] == selector[3]
            ) {
                return true;
            }
        }

        return false;
    }
}
