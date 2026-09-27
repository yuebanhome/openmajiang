"""Private-observation Bot SDK; long-lived API keys never enter URLs or logs."""
from .client import BotClient, ControlTransferredError, DecisionEngine, StrategyContext, first_legal

__all__ = ["BotClient", "ControlTransferredError", "DecisionEngine", "StrategyContext", "first_legal"]
