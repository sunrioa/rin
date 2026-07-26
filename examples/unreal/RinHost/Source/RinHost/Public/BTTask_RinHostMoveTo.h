#pragma once

#include "CoreMinimal.h"
#include "BehaviorTree/Tasks/BTTask_MoveTo.h"
#include "BehaviorTree/Blackboard/BlackboardKeyType_String.h"
#include "RinHostTypes.h"
#include "BTTask_RinHostMoveTo.generated.h"

UCLASS()
class RINHOST_API UBTTask_RinHostMoveTo final : public UBTTask_MoveTo
{
    GENERATED_BODY()

public:
    UBTTask_RinHostMoveTo();

protected:
    virtual EBTNodeResult::Type ExecuteTask(
        UBehaviorTreeComponent& OwnerComponent,
        uint8* NodeMemory
    ) override;

    virtual void OnTaskFinished(
        UBehaviorTreeComponent& OwnerComponent,
        uint8* NodeMemory,
        EBTNodeResult::Type TaskResult
    ) override;

    UPROPERTY(EditAnywhere, Category = "Rin")
    FBlackboardKeySelector OperationIdKey;

private:
    FString ActiveOperationId;
    FRinHostEpoch ActiveEpoch;
};
