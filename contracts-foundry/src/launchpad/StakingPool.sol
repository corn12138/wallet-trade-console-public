// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/utils/Pausable.sol";

/**
 * @title StakingPool
 * @dev Allows users to stake ERC20 tokens and earn rewards over time.
 *
 * Features:
 * - Stake/unstake any ERC20 token
 * - Time-based reward distribution (rewardRate per second)
 * - Emergency withdrawal (forfeits pending rewards)
 * - Owner deposits reward tokens
 * - Pause functionality
 */
contract StakingPool is ReentrancyGuard, Ownable, Pausable {
    using SafeERC20 for IERC20;

    IERC20 public stakingToken;
    IERC20 public rewardToken;

    // Reward state
    uint256 public rewardRate; // Rewards per second per staked token (scaled by 1e18)
    uint256 public rewardPerTokenStored;
    uint256 public lastUpdateTime;
    uint256 public rewardsDuration; // Duration of the reward period in seconds
    uint256 public periodFinish; // When rewards end

    // Pool info
    uint256 public totalStaked;
    uint256 public minStakeAmount;
    uint256 public maxStakePerUser; // 0 = no limit

    // User data
    mapping(address => uint256) public stakedBalance;
    mapping(address => uint256) public userRewardPerTokenPaid;
    mapping(address => uint256) public rewards;

    // Events
    event Staked(address indexed user, uint256 amount);
    event Unstaked(address indexed user, uint256 amount);
    event RewardClaimed(address indexed user, uint256 reward);
    event RewardAdded(uint256 reward);
    event EmergencyWithdraw(address indexed user, uint256 amount);

    constructor(
        address _stakingToken,
        address _rewardToken,
        uint256 _rewardsDuration
    ) Ownable(msg.sender) {
        stakingToken = IERC20(_stakingToken);
        rewardToken = IERC20(_rewardToken);
        rewardsDuration = _rewardsDuration;
    }

    // ============ Modifiers ============

    modifier updateReward(address account) {
        rewardPerTokenStored = rewardPerToken();
        lastUpdateTime = lastTimeRewardApplicable();
        if (account != address(0)) {
            rewards[account] = earned(account);
            userRewardPerTokenPaid[account] = rewardPerTokenStored;
        }
        _;
    }

    // ============ View Functions ============

    function lastTimeRewardApplicable() public view returns (uint256) {
        return block.timestamp < periodFinish ? block.timestamp : periodFinish;
    }

    function rewardPerToken() public view returns (uint256) {
        if (totalStaked == 0) {
            return rewardPerTokenStored;
        }
        return
            rewardPerTokenStored +
            (((lastTimeRewardApplicable() - lastUpdateTime) *
                rewardRate *
                1e18) / totalStaked);
    }

    function earned(address account) public view returns (uint256) {
        return
            ((stakedBalance[account] *
                (rewardPerToken() - userRewardPerTokenPaid[account])) / 1e18) +
            rewards[account];
    }

    function getRewardForDuration() external view returns (uint256) {
        return rewardRate * rewardsDuration;
    }

    /**
     * @dev APY estimation (basis points, 10000 = 100%)
     */
    function estimatedAPY() external view returns (uint256) {
        if (totalStaked == 0) return 0;
        uint256 yearlyRewards = rewardRate * 365 days;
        return (yearlyRewards * 10000) / totalStaked;
    }

    // ============ User Actions ============

    /**
     * @dev Stake tokens
     */
    function stake(
        uint256 amount
    ) external nonReentrant whenNotPaused updateReward(msg.sender) {
        require(amount > 0, "Cannot stake 0");
        if (minStakeAmount > 0) {
            require(amount >= minStakeAmount, "Below minimum stake");
        }
        if (maxStakePerUser > 0) {
            require(
                stakedBalance[msg.sender] + amount <= maxStakePerUser,
                "Exceeds max stake"
            );
        }

        totalStaked += amount;
        stakedBalance[msg.sender] += amount;
        stakingToken.safeTransferFrom(msg.sender, address(this), amount);

        emit Staked(msg.sender, amount);
    }

    /**
     * @dev Unstake tokens and claim rewards
     */
    function unstake(
        uint256 amount
    ) external nonReentrant updateReward(msg.sender) {
        require(amount > 0, "Cannot unstake 0");
        require(
            stakedBalance[msg.sender] >= amount,
            "Insufficient staked balance"
        );

        totalStaked -= amount;
        stakedBalance[msg.sender] -= amount;
        stakingToken.safeTransfer(msg.sender, amount);

        emit Unstaked(msg.sender, amount);
    }

    /**
     * @dev Claim accumulated rewards
     */
    function claimReward() external nonReentrant updateReward(msg.sender) {
        uint256 reward = rewards[msg.sender];
        if (reward > 0) {
            rewards[msg.sender] = 0;
            rewardToken.safeTransfer(msg.sender, reward);
            emit RewardClaimed(msg.sender, reward);
        }
    }

    /**
     * @dev Unstake all and claim rewards in one transaction
     */
    function exit() external nonReentrant updateReward(msg.sender) {
        uint256 staked = stakedBalance[msg.sender];
        uint256 reward = rewards[msg.sender];

        // Update state before external calls (CEI pattern)
        if (staked > 0) {
            totalStaked -= staked;
            stakedBalance[msg.sender] = 0;
        }
        if (reward > 0) {
            rewards[msg.sender] = 0;
        }

        // External calls after all state updates
        if (staked > 0) {
            stakingToken.safeTransfer(msg.sender, staked);
            emit Unstaked(msg.sender, staked);
        }
        if (reward > 0) {
            rewardToken.safeTransfer(msg.sender, reward);
            emit RewardClaimed(msg.sender, reward);
        }
    }

    /**
     * @dev Emergency withdraw (forfeits rewards)
     */
    function emergencyWithdraw() external nonReentrant {
        uint256 staked = stakedBalance[msg.sender];
        require(staked > 0, "Nothing staked");

        totalStaked -= staked;
        stakedBalance[msg.sender] = 0;
        rewards[msg.sender] = 0;
        userRewardPerTokenPaid[msg.sender] = rewardPerTokenStored;

        stakingToken.safeTransfer(msg.sender, staked);
        emit EmergencyWithdraw(msg.sender, staked);
    }

    // ============ Admin Functions ============

    /**
     * @dev Deposit reward tokens and start reward period
     */
    function notifyRewardAmount(
        uint256 reward
    ) external onlyOwner updateReward(address(0)) {
        rewardToken.safeTransferFrom(msg.sender, address(this), reward);

        if (block.timestamp >= periodFinish) {
            rewardRate = reward / rewardsDuration;
        } else {
            uint256 remaining = periodFinish - block.timestamp;
            uint256 leftover = remaining * rewardRate;
            rewardRate = (reward + leftover) / rewardsDuration;
        }

        lastUpdateTime = block.timestamp;
        periodFinish = block.timestamp + rewardsDuration;

        emit RewardAdded(reward);
    }

    /**
     * @dev Update rewards duration (only when period ended)
     */
    function setRewardsDuration(uint256 _rewardsDuration) external onlyOwner {
        require(block.timestamp > periodFinish, "Reward period not finished");
        rewardsDuration = _rewardsDuration;
    }

    /**
     * @dev Set minimum stake amount
     */
    function setMinStakeAmount(uint256 _min) external onlyOwner {
        minStakeAmount = _min;
    }

    /**
     * @dev Set maximum stake per user
     */
    function setMaxStakePerUser(uint256 _max) external onlyOwner {
        maxStakePerUser = _max;
    }

    /**
     * @dev Emergency pause
     */
    function pause() external onlyOwner {
        _pause();
    }

    /**
     * @dev Unpause
     */
    function unpause() external onlyOwner {
        _unpause();
    }

    /**
     * @dev Recover accidentally sent tokens (not staking or reward tokens)
     */
    function recoverToken(
        address tokenAddress,
        uint256 amount
    ) external onlyOwner {
        require(
            tokenAddress != address(stakingToken),
            "Cannot recover staking token"
        );
        require(
            tokenAddress != address(rewardToken),
            "Cannot recover reward token"
        );
        IERC20(tokenAddress).safeTransfer(owner(), amount);
    }
}
