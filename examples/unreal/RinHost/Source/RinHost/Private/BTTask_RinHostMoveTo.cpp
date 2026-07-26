#include "BTTask_RinHostMoveTo.h"

#include "BehaviorTree/BlackboardComponent.h"
#include "Engine/GameInstance.h"
#include "RinHostSubsystem.h"

UBTTask_RinHostMoveTo::UBTTask_RinHostMoveTo()
{
    NodeName = TEXT("Rin Host Move To");
    bCreateNodeInstance = true;
    bNotifyTaskFinished = true;
    OperationIdKey.AddStringFilter(
        this,
        GET_MEMBER_NAME_CHECKED(UBTTask_RinHostMoveTo, OperationIdKey)
    );
}

EBTNodeResult::Type UBTTask_RinHostMoveTo::ExecuteTask(
    UBehaviorTreeComponent& OwnerComponent,
    uint8* NodeMemory
)
{
    UBlackboardComponent* Blackboard = OwnerComponent.GetBlackboardComponent();
    UWorld* World = OwnerComponent.GetWorld();
    if (Blackboard == nullptr || World == nullptr)
    {
        return EBTNodeResult::Failed;
    }
    ActiveOperationId = Blackboard->GetValueAsString(
        OperationIdKey.SelectedKeyName
    );
    UGameInstance* GameInstance = World->GetGameInstance();
    URinHostSubsystem* Host = GameInstance == nullptr
        ? nullptr
        : GameInstance->GetSubsystem<URinHostSubsystem>();
    if (Host == nullptr)
    {
        return EBTNodeResult::Failed;
    }
    ActiveEpoch = Host->CurrentEpoch();
    if (!Host->ReportRun(
            ActiveOperationId,
            ActiveEpoch,
            ERinActionRunStatus::Running,
            2,
            1,
            TEXT("Behavior Tree movement started.")
        ))
    {
        return EBTNodeResult::Failed;
    }
    return Super::ExecuteTask(OwnerComponent, NodeMemory);
}

void UBTTask_RinHostMoveTo::OnTaskFinished(
    UBehaviorTreeComponent& OwnerComponent,
    uint8* NodeMemory,
    const EBTNodeResult::Type TaskResult
)
{
    if (UWorld* World = OwnerComponent.GetWorld())
    {
        if (UGameInstance* GameInstance = World->GetGameInstance())
        {
            if (URinHostSubsystem* Host =
                GameInstance->GetSubsystem<URinHostSubsystem>())
            {
                const bool bSucceeded =
                    TaskResult == EBTNodeResult::Succeeded;
                const bool bCancelled =
                    TaskResult == EBTNodeResult::Aborted;
                Host->ReportRun(
                    ActiveOperationId,
                    ActiveEpoch,
                    bSucceeded
                        ? ERinActionRunStatus::Succeeded
                        : bCancelled
                            ? ERinActionRunStatus::Cancelled
                            : ERinActionRunStatus::Failed,
                    3,
                    bSucceeded ? 100 : 1,
                    bSucceeded
                        ? TEXT("Behavior Tree movement completed.")
                        : bCancelled
                            ? TEXT("Behavior Tree movement cancelled.")
                            : TEXT("Behavior Tree movement failed.")
                );
            }
        }
    }
    ActiveOperationId.Reset();
    ActiveEpoch = FRinHostEpoch();
    Super::OnTaskFinished(OwnerComponent, NodeMemory, TaskResult);
}
