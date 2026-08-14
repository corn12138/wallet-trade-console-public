// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import "@openzeppelin/contracts/access/AccessControl.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/utils/Pausable.sol";

/// @title BridgeGateway — lock-and-release cross-chain transfer gateway.
///
/// @notice ONE contract, deployed on BOTH chains of a route. On the source
/// chain it escrows the user's tokens and emits `BridgeInitiated`; on the
/// destination chain a relayer calls `fulfill` to release pre-funded liquidity
/// to the recipient.
///
/// ── TRUST MODEL (read this before using it for anything that matters) ───────
/// This is a TRUSTED-RELAYER bridge, not a light-client or proof-based one.
/// The destination gateway does NOT verify a source-chain proof; it trusts an
/// address holding RELAYER_ROLE to assert that a deposit happened. That means:
///
///   - a malicious or compromised relayer can drain destination liquidity;
///   - a relayer that goes offline strands deposits (recoverable only by
///     `refund`, which is deliberately admin-gated and audited by event);
///   - destination liquidity is provided by the operator, not minted — a
///     transfer larger than the available balance simply reverts.
///
/// These are real properties of the design, not omissions. The product surfaces
/// them: the API reports the trust model and live liquidity, and the UI must
/// not present a transfer as trustless.
///
/// Replay protection is by `transferId`, derived deterministically on the
/// SOURCE chain from (srcChainId, srcGateway, nonce), so the destination can
/// mark exactly one fulfillment per deposit.
contract BridgeGateway is AccessControl, ReentrancyGuard, Pausable {
    using SafeERC20 for IERC20;

    bytes32 public constant RELAYER_ROLE = keccak256("RELAYER_ROLE");

    /// @notice Monotonic per-gateway deposit counter, used to derive transferId.
    uint256 public nonce;

    /// @notice srcToken => dstChainId => dstToken. A route is supported only
    /// when this mapping is set; deposits on an unmapped route revert rather
    /// than escrowing funds the relayer could never deliver.
    mapping(address => mapping(uint256 => address)) public routeToken;

    /// @notice Minimum transfer per source token. Zero means "route disabled"
    /// is expressed by routeToken, not here; a dust deposit that costs more in
    /// relayer gas than it moves is rejected outright.
    mapping(address => uint256) public minAmount;

    /// @notice transferId => fulfilled. Destination-side replay protection.
    mapping(bytes32 => bool) public fulfilled;

    /// @notice transferId => refunded. Source-side, mutually exclusive with a
    /// successful delivery in practice; the operator must confirm the
    /// destination never fulfilled before refunding.
    mapping(bytes32 => bool) public refunded;

    /// @notice transferId => escrowed deposit, kept so a refund can return the
    /// exact amount to the exact depositor without trusting call data.
    struct Deposit {
        address sender;
        address token;
        uint256 amount;
        uint256 dstChainId;
        bool exists;
    }

    mapping(bytes32 => Deposit) public deposits;

    event RouteSet(address indexed srcToken, uint256 indexed dstChainId, address dstToken, uint256 minAmount);
    event BridgeInitiated(
        bytes32 indexed transferId,
        address indexed sender,
        address indexed recipient,
        address srcToken,
        uint256 amount,
        uint256 srcChainId,
        uint256 dstChainId,
        uint256 depositNonce
    );
    event BridgeFulfilled(
        bytes32 indexed transferId,
        address indexed recipient,
        address dstToken,
        uint256 amount,
        uint256 srcChainId
    );
    event BridgeRefunded(bytes32 indexed transferId, address indexed sender, address token, uint256 amount);
    event LiquidityAdded(address indexed token, address indexed from, uint256 amount);
    event LiquidityWithdrawn(address indexed token, address indexed to, uint256 amount);

    error RouteNotSupported(address srcToken, uint256 dstChainId);
    error AmountBelowMinimum(uint256 amount, uint256 required);
    error TransferAlreadyFulfilled(bytes32 transferId);
    error TransferAlreadyRefunded(bytes32 transferId);
    error UnknownTransfer(bytes32 transferId);
    error InsufficientLiquidity(address token, uint256 requested, uint256 available);
    error ZeroAddress();
    error SameChain();

    constructor(address admin) {
        if (admin == address(0)) revert ZeroAddress();
        _grantRole(DEFAULT_ADMIN_ROLE, admin);
    }

    // ─── Admin ───────────────────────────────────────────────────────────────

    /// @notice Enable a route. `dstToken` is the token the DESTINATION gateway
    /// releases; set it to address(0) to disable the route.
    function setRoute(address srcToken, uint256 dstChainId, address dstToken, uint256 minimumAmount)
        external
        onlyRole(DEFAULT_ADMIN_ROLE)
    {
        if (srcToken == address(0)) revert ZeroAddress();
        if (dstChainId == block.chainid) revert SameChain();
        routeToken[srcToken][dstChainId] = dstToken;
        minAmount[srcToken] = minimumAmount;
        emit RouteSet(srcToken, dstChainId, dstToken, minimumAmount);
    }

    function pause() external onlyRole(DEFAULT_ADMIN_ROLE) {
        _pause();
    }

    function unpause() external onlyRole(DEFAULT_ADMIN_ROLE) {
        _unpause();
    }

    /// @notice Fund destination-side liquidity. Kept as an explicit, evented
    /// entry point rather than a bare transfer so the operator's funding is
    /// auditable on-chain.
    function addLiquidity(address token, uint256 amount) external nonReentrant {
        if (token == address(0)) revert ZeroAddress();
        IERC20(token).safeTransferFrom(msg.sender, address(this), amount);
        emit LiquidityAdded(token, msg.sender, amount);
    }

    /// @notice Withdraw operator liquidity. Admin-only and evented.
    /// @dev This CAN withdraw escrowed user deposits — the trust model above
    /// already grants the admin that power, and hiding it behind a separate
    /// accounting split would be security theatre, not a guarantee.
    function withdrawLiquidity(address token, address to, uint256 amount)
        external
        onlyRole(DEFAULT_ADMIN_ROLE)
        nonReentrant
    {
        if (to == address(0)) revert ZeroAddress();
        IERC20(token).safeTransfer(to, amount);
        emit LiquidityWithdrawn(token, to, amount);
    }

    // ─── Source chain ────────────────────────────────────────────────────────

    /// @notice Escrow `amount` of `srcToken` for delivery on `dstChainId`.
    /// @return transferId the deterministic id the relayer will fulfill.
    function deposit(address srcToken, uint256 amount, uint256 dstChainId, address recipient)
        external
        nonReentrant
        whenNotPaused
        returns (bytes32 transferId)
    {
        if (recipient == address(0)) revert ZeroAddress();
        if (dstChainId == block.chainid) revert SameChain();
        if (routeToken[srcToken][dstChainId] == address(0)) {
            revert RouteNotSupported(srcToken, dstChainId);
        }
        uint256 minimum = minAmount[srcToken];
        if (amount < minimum || amount == 0) {
            revert AmountBelowMinimum(amount, minimum);
        }

        uint256 depositNonce = ++nonce;
        transferId = computeTransferId(block.chainid, address(this), depositNonce);

        // Measure the actual delta so a fee-on-transfer token can never cause
        // the destination to release more than this gateway truly received.
        uint256 before = IERC20(srcToken).balanceOf(address(this));
        IERC20(srcToken).safeTransferFrom(msg.sender, address(this), amount);
        uint256 received = IERC20(srcToken).balanceOf(address(this)) - before;

        deposits[transferId] = Deposit({
            sender: msg.sender,
            token: srcToken,
            amount: received,
            dstChainId: dstChainId,
            exists: true
        });

        emit BridgeInitiated(
            transferId, msg.sender, recipient, srcToken, received, block.chainid, dstChainId, depositNonce
        );
    }

    /// @notice Return an escrowed deposit to its sender.
    /// @dev Admin-gated on purpose: only the operator can know that the
    /// destination chain never fulfilled this transfer. Refunding a transfer
    /// that WAS delivered would pay it out twice, and no on-chain check here
    /// can rule that out — which is precisely the cost of the trust model.
    function refund(bytes32 transferId) external onlyRole(DEFAULT_ADMIN_ROLE) nonReentrant {
        Deposit storage d = deposits[transferId];
        if (!d.exists) revert UnknownTransfer(transferId);
        if (refunded[transferId]) revert TransferAlreadyRefunded(transferId);

        refunded[transferId] = true;
        IERC20(d.token).safeTransfer(d.sender, d.amount);
        emit BridgeRefunded(transferId, d.sender, d.token, d.amount);
    }

    // ─── Destination chain ───────────────────────────────────────────────────

    /// @notice Release `amount` of `dstToken` to `recipient` for a deposit that
    /// happened on `srcChainId`. Relayer-only, and exactly once per transferId.
    function fulfill(
        bytes32 transferId,
        address dstToken,
        address recipient,
        uint256 amount,
        uint256 srcChainId
    ) external onlyRole(RELAYER_ROLE) nonReentrant whenNotPaused {
        if (recipient == address(0) || dstToken == address(0)) revert ZeroAddress();
        if (fulfilled[transferId]) revert TransferAlreadyFulfilled(transferId);

        uint256 available = IERC20(dstToken).balanceOf(address(this));
        if (available < amount) {
            // Revert rather than partially fill: a half-delivered transfer has
            // no honest representation in the status projection.
            revert InsufficientLiquidity(dstToken, amount, available);
        }

        // Effects before interactions — the id is burned even if the token's
        // transfer hook tries to re-enter.
        fulfilled[transferId] = true;

        IERC20(dstToken).safeTransfer(recipient, amount);
        emit BridgeFulfilled(transferId, recipient, dstToken, amount, srcChainId);
    }

    // ─── Views ───────────────────────────────────────────────────────────────

    /// @notice The deterministic transfer id for a source-chain deposit.
    /// @dev Deriving it from (srcChainId, srcGateway, nonce) makes it unique
    /// across chains and across redeployments of the gateway, so a destination
    /// can never confuse two different routes' transfers.
    function computeTransferId(uint256 srcChainId, address srcGateway, uint256 depositNonce)
        public
        pure
        returns (bytes32)
    {
        return keccak256(abi.encode(srcChainId, srcGateway, depositNonce));
    }

    /// @notice Liquidity currently available to fulfill transfers in `token`.
    function availableLiquidity(address token) external view returns (uint256) {
        return IERC20(token).balanceOf(address(this));
    }

    /// @notice Whether a route is currently usable, and what it delivers.
    function route(address srcToken, uint256 dstChainId)
        external
        view
        returns (bool supported, address dstToken, uint256 minimumAmount)
    {
        dstToken = routeToken[srcToken][dstChainId];
        return (dstToken != address(0), dstToken, minAmount[srcToken]);
    }
}
