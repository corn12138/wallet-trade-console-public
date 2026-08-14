// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Script, console} from "forge-std/Script.sol";
import {BridgeGateway} from "../src/bridge/BridgeGateway.sol";

/// Deploys a BridgeGateway and optionally opens one route.
///
/// The SAME script runs on every chain of a route — that is the whole point of
/// the design: a gateway is symmetric, so adding a second chain later is a
/// deploy + config step, not a code change.
///
/// Env:
///   PRIVATE_KEY            deployer/admin key (required)
///   BRIDGE_RELAYER         address to grant RELAYER_ROLE (optional; defaults
///                          to the deployer so a single-operator setup works
///                          out of the box — rotate it before anything real)
///   BRIDGE_ROUTE_TOKEN     src token to open a route for (optional)
///   BRIDGE_ROUTE_DST_CHAIN destination chain id for that route (optional)
///   BRIDGE_ROUTE_DST_TOKEN token the DESTINATION gateway releases (optional)
///   BRIDGE_ROUTE_MIN       minimum transfer, base units (optional, default 0)
///
/// A route is only opened when TOKEN + DST_CHAIN + DST_TOKEN are all set. That
/// keeps the first-chain deploy honest: with no counterpart deployed yet there
/// is no route to open, and `deposit` correctly reverts as unsupported rather
/// than escrowing funds nothing can deliver.
contract DeployBridgeScript is Script {
    function run() external {
        uint256 pk = vm.envUint("PRIVATE_KEY");
        address deployer = vm.addr(pk);
        address relayer = vm.envOr("BRIDGE_RELAYER", deployer);

        vm.startBroadcast(pk);

        BridgeGateway gateway = new BridgeGateway(deployer);
        gateway.grantRole(gateway.RELAYER_ROLE(), relayer);

        address routeToken = vm.envOr("BRIDGE_ROUTE_TOKEN", address(0));
        uint256 dstChain = vm.envOr("BRIDGE_ROUTE_DST_CHAIN", uint256(0));
        address dstToken = vm.envOr("BRIDGE_ROUTE_DST_TOKEN", address(0));
        uint256 minAmount = vm.envOr("BRIDGE_ROUTE_MIN", uint256(0));

        bool routeOpened = routeToken != address(0) && dstChain != 0 && dstToken != address(0);
        if (routeOpened) {
            gateway.setRoute(routeToken, dstChain, dstToken, minAmount);
        }

        vm.stopBroadcast();

        console.log("chainId        ", block.chainid);
        console.log("BridgeGateway  ", address(gateway));
        console.log("admin          ", deployer);
        console.log("relayer        ", relayer);
        if (routeOpened) {
            console.log("route srcToken ", routeToken);
            console.log("route dstChain ", dstChain);
            console.log("route dstToken ", dstToken);
            console.log("route minAmount", minAmount);
        } else {
            console.log("route          <none opened - set BRIDGE_ROUTE_* once the counterpart chain exists>");
        }
    }
}
